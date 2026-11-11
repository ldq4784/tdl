package up

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/expr-lang/expr/vm"
	"github.com/fatih/color"
	"github.com/gabriel-vasile/mimetype"
	"github.com/go-faster/errors"
	"github.com/go-viper/mapstructure/v2"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/core/uploader"
	"github.com/iyear/tdl/core/util/mediautil"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/texpr"
)

type File struct {
	File  string
	Thumb string
}

type dest struct {
	Peer   string
	Thread int
}

type iter struct {
	files     []*File
	to        *vm.Program
	caption   *vm.Program
	chat      string
	topic     int
	photo     bool
	remove    bool
	group     bool
	groupSize int
	delay     time.Duration
	manager   *peers.Manager

	cur     int
	pending *iterElem
	started bool
	err     error
	file    uploader.Elem
}

func newIter(files []*File, to, caption *vm.Program, chat string, topic int, photo, remove, group bool, groupSize int, delay time.Duration, manager *peers.Manager) *iter {
	return &iter{
		files:     files,
		to:        to,
		caption:   caption,
		chat:      chat,
		topic:     topic,
		photo:     photo,
		remove:    remove,
		group:     group,
		groupSize: groupSize,
		delay:     delay,
		manager:   manager,

		cur:     0,
		pending: nil,
		started: false,
		err:     nil,
		file:    nil,
	}
}

func (i *iter) Next(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		i.err = ctx.Err()
		return false
	default:
	}

	if (i.cur >= len(i.files) && i.pending == nil) || i.err != nil {
		return false
	}

	// if delay is set, sleep for a while for each iteration
	if i.delay > 0 && i.started { // skip first delay
		time.Sleep(i.delay)
	}
	i.started = true

	if i.group {
		file, err := i.nextGroup(ctx)
		if err != nil {
			i.err = err
			return false
		}
		if file == nil {
			return false
		}
		i.file = file
		return true
	}

	cur := i.files[i.cur]
	i.cur++

	file, err := i.next(ctx, cur)
	if err != nil {
		i.err = err
		return false
	}

	i.file = file
	return true
}

func (i *iter) nextGroup(ctx context.Context) (uploader.Elem, error) {
	if i.cur >= len(i.files) && i.pending == nil {
		return nil, nil
	}

	limit := i.groupSize
	if limit <= 0 || limit > 10 {
		limit = 10
	}

	var groupElems []uploader.Elem
	var firstTo tg.InputPeerClass
	var firstThread int

	for len(groupElems) < limit {
		var elem *iterElem
		var err error

		if i.pending != nil {
			elem = i.pending
			i.pending = nil
		} else if i.cur < len(i.files) {
			cur := i.files[i.cur]
			i.cur++
			elem, err = i.next(ctx, cur)
			if err != nil {
				return nil, err
			}
		} else {
			break
		}

		if len(groupElems) == 0 {
			firstTo = elem.To()
			firstThread = elem.Thread()
			groupElems = append(groupElems, elem)
		} else {
			if !reflect.DeepEqual(firstTo, elem.To()) || firstThread != elem.Thread() {
				i.pending = elem
				break
			}
			groupElems = append(groupElems, elem)
		}
	}

	if len(groupElems) == 0 {
		return nil, nil
	}

	return &groupElem{
		elems:  groupElems,
		to:     firstTo,
		thread: firstThread,
	}, nil
}

func (i *iter) next(ctx context.Context, cur *File) (*iterElem, error) {
	file, err := i.resolveFile(cur.File)
	if err != nil {
		return nil, errors.Wrap(err, "resolve file")
	}

	env := exprEnv(ctx, cur)

	to, thread, err := i.resolveDest(ctx, env)
	if err != nil {
		return nil, errors.Wrap(err, "resolve destination")
	}

	caption, err := i.resolveCaption(env)
	if err != nil {
		return nil, errors.Wrap(err, "resolve caption")
	}

	var isTempThumb bool
	thumbPath := cur.Thumb
	var videoW, videoH int
	var videoDur float64

	if mediautil.IsVideo(env.MIME) {
		if meta, err := mediautil.ProbeVideo(cur.File); err == nil {
			videoW = meta.W
			videoH = meta.H
			videoDur = meta.Duration
		}

		if thumbPath == "" {
			color.Cyan("[%s] Extracting video thumbnail with ffmpeg...", filepath.Base(cur.File))
			if t, err := mediautil.ExtractThumbnail(cur.File); err == nil && t != "" {
				thumbPath = t
				isTempThumb = true
				color.Green("[%s] Video thumbnail generated successfully", filepath.Base(cur.File))
			} else if err != nil {
				color.Yellow("[%s] Video thumbnail extraction failed: %v", filepath.Base(cur.File), err)
			}
		}
	}

	thumb, err := i.resolveThumb(thumbPath)
	if err != nil {
		if isTempThumb {
			_ = os.Remove(thumbPath)
		}
		return nil, errors.Wrap(err, "resolve thumbnail")
	}

	return &iterElem{
		file:        file,
		thumb:       thumb,
		to:          to,
		caption:     caption,
		thread:      thread,
		asPhoto:     i.photo,
		remove:      i.remove,
		isTempThumb: isTempThumb,
		videoW:      videoW,
		videoH:      videoH,
		videoDur:    videoDur,
	}, nil
}

func (i *iter) resolveFile(path string) (*uploaderFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "open file")
	}

	stat, err := f.Stat()
	if err != nil {
		return nil, errors.Wrap(err, "stat file")
	}

	return &uploaderFile{
		File: f,
		size: stat.Size(),
	}, nil
}

func (i *iter) resolveDest(ctx context.Context, env Env) (peers.Peer, int, error) {
	if i.chat != "" { // compatible with old version
		to, err := i.resolvePeer(ctx, i.chat)
		if err != nil {
			return nil, 0, errors.Wrap(err, "resolve chat")
		}

		return to, i.topic, nil
	}

	// message routing
	result, err := texpr.Run(i.to, env)
	if err != nil {
		return nil, 0, errors.Wrap(err, "parse expression")
	}

	var (
		to     peers.Peer
		thread int
	)

	switch r := result.(type) {
	case string:
		// pure chat, no reply to, which is a compatible with old version
		// and a convenient way to send message to self
		to, err = i.resolvePeer(ctx, r)
	case map[string]interface{}:
		// chat with reply to topic or message
		var d dest

		if err = mapstructure.WeakDecode(r, &d); err != nil {
			return nil, 0, errors.Wrapf(err, "decode dest: %v", result)
		}

		to, err = i.resolvePeer(ctx, d.Peer)
		thread = d.Thread
	default:
		return nil, 0, errors.Errorf("message router must return string or dest: %T", result)
	}

	if err != nil {
		return nil, 0, errors.Wrap(err, "resolve peer")
	}

	return to, thread, nil
}

func (i *iter) resolvePeer(ctx context.Context, peer string) (peers.Peer, error) {
	if peer == "" { // self
		return i.manager.Self(ctx)
	}

	return tutil.GetInputPeer(ctx, i.manager, peer)
}

func (i *iter) resolveCaption(env Env) (*entity.Builder, error) {
	// parse caption
	captionStr, err := texpr.Run(i.caption, env)
	if err != nil {
		return nil, errors.Wrap(err, "parse caption")
	}

	r, ok := captionStr.(string)
	if !ok {
		return nil, errors.Errorf("caption must return string, got %T", captionStr)
	}

	caption := &entity.Builder{}
	if len(r) > 0 {
		if err = html.HTML(strings.NewReader(r), caption, html.Options{
			UserResolver:          nil,
			DisableTelegramEscape: false,
		}); err != nil {
			return nil, errors.Wrap(err, "parse caption HTML")
		}
	}

	return caption, nil
}

func (i *iter) resolveThumb(path string) (*uploaderFile, error) {
	if path == "" {
		return nil, nil
	}

	// has thumbnail
	mime, err := mimetype.DetectFile(path)
	if err != nil || !mediautil.IsImage(mime.String()) { // TODO(iyear): jpg only
		return nil, errors.Wrapf(err, "invalid thumbnail file: %v", path)
	}

	thumb, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "open thumbnail file")
	}

	return &uploaderFile{
		File: thumb,
		size: 0,
	}, nil
}

func (i *iter) Value() uploader.Elem {
	return i.file
}

func (i *iter) Err() error {
	return i.err
}
