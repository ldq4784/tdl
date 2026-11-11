package uploader

import (
	"context"
	crand "crypto/rand"
	"io"
	"math"
	"math/big"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/go-faster/errors"
	"github.com/fatih/color"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/samber/lo"
	"golang.org/x/sync/errgroup"

	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/fsutil"
	"github.com/iyear/tdl/core/util/mediautil"
)

// MaxPartSize refer to https://core.telegram.org/api/files#uploading-files
const MaxPartSize = 512 * 1024

type Uploader struct {
	opts Options
}

type Options struct {
	Client   *tg.Client
	Threads  int
	Iter     Iter
	Progress Progress
}

func New(o Options) *Uploader {
	return &Uploader{opts: o}
}

func randID() int64 {
	n, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		return time.Now().UnixNano()
	}
	return n.Int64()
}

func (u *Uploader) Upload(ctx context.Context, limit int) error {
	wg, wgctx := errgroup.WithContext(ctx)
	wg.SetLimit(limit)

	for u.opts.Iter.Next(wgctx) {
		elem := u.opts.Iter.Value()

		wg.Go(func() (rerr error) {
			if group, ok := elem.(GroupElem); ok {
				for _, item := range group.Elems() {
					u.opts.Progress.OnAdd(item)
				}
				defer func() {
					for _, item := range group.Elems() {
						u.opts.Progress.OnDone(item, rerr)
					}
				}()

				if err := u.uploadGroup(wgctx, group); err != nil {
					rerr = err
					color.Red("\nUpload album error: %s\n", err.Error())
					return errors.Wrap(err, "upload album")
				}
				return nil
			}

			u.opts.Progress.OnAdd(elem)
			defer func() { u.opts.Progress.OnDone(elem, rerr) }()

			if err := u.upload(wgctx, elem); err != nil {
				rerr = err
				// canceled by user, so we directly return error to stop all
				if errors.Is(err, context.Canceled) {
					return errors.Wrap(err, "upload")
				}

				// don't return error, just log it
			}

			return nil
		})
	}

	if err := u.opts.Iter.Err(); err != nil {
		return errors.Wrap(err, "iter")
	}

	return wg.Wait()
}

func (u *Uploader) upload(ctx context.Context, elem Elem) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	up := uploader.NewUploader(u.opts.Client).
		WithPartSize(MaxPartSize).
		WithThreads(u.opts.Threads).
		WithProgress(&wrapProcess{
			elem:    elem,
			process: u.opts.Progress,
		})

	f, err := up.Upload(ctx, uploader.NewUpload(elem.File().Name(), elem.File(), elem.File().Size()))
	if err != nil {
		return errors.Wrap(err, "upload file")
	}

	if _, err = elem.File().Seek(0, io.SeekStart); err != nil {
		return errors.Wrap(err, "seek file")
	}
	mime, err := mimetype.DetectReader(elem.File())
	if err != nil {
		return errors.Wrap(err, "detect mime")
	}

	// here convert underlying entities to formatters for message caption
	caption := styling.Custom(func(eb *entity.Builder) error {
		msg, entities := elem.Caption()
		eb.Format(msg, lo.Map(entities, func(item tg.MessageEntityClass, _ int) entity.Formatter {
			return func(_, _ int) tg.MessageEntityClass {
				return item
			}
		})...)
		return nil
	})

	doc := message.UploadedDocument(f, caption).MIME(mime.String()).Filename(elem.File().Name())
	// upload thumbnail
	if thumb, ok := elem.Thumb(); ok {
		if _, err := thumb.Seek(0, io.SeekStart); err == nil {
			thumbBytes, err := io.ReadAll(thumb)
			if err == nil && len(thumbBytes) > 0 {
				thumbFile, err := uploader.NewUploader(u.opts.Client).
					FromBytes(ctx, "thumb.jpg", thumbBytes)
				if err == nil {
					doc = doc.Thumb(thumbFile)
				} else {
					color.Yellow("[%s] Upload thumbnail failed: %v", elem.File().Name(), err)
				}
			}
		}
	}

	var media message.MediaOption = doc

	switch {
	case mediautil.IsImage(mime.String()) && elem.AsPhoto():
		// webp should be uploaded as document
		if mime.String() == "image/webp" {
			break
		}
		// upload as photo
		media = message.UploadedPhoto(f, caption)
	case mediautil.IsVideo(mime.String()):
		// reset reader
		if _, err = elem.File().Seek(0, io.SeekStart); err != nil {
			return errors.Wrap(err, "seek file")
		}
		videoDoc := doc.Video().SupportsStreaming()
		if vp, ok := elem.(VideoMetaProvider); ok {
			if w, h, dur, ok := vp.VideoMeta(); ok {
				videoDoc = videoDoc.Duration(time.Duration(dur) * time.Second).Resolution(w, h)
			}
		} else if dur, w, h, err := mediautil.GetMP4Info(elem.File()); err == nil {
			videoDoc = videoDoc.Duration(time.Duration(dur) * time.Second).Resolution(w, h)
		}
		media = videoDoc
	case mediautil.IsAudio(mime.String()):
		media = doc.Audio().Title(fsutil.GetNameWithoutExt(elem.File().Name()))
	}

	_, err = message.NewSender(u.opts.Client).
		WithUploader(up).
		To(elem.To()).
		Reply(elem.Thread()).
		Media(ctx, media)
	if err != nil {
		return errors.Wrap(err, "send message")
	}

	return nil
}

func (u *Uploader) uploadGroup(ctx context.Context, group GroupElem) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	elems := group.Elems()
	if len(elems) == 0 {
		return nil
	}

	results := make([]tg.InputSingleMedia, len(elems))

	// 使用 errgroup 并发上传组内的所有媒体文件
	g, gctx := errgroup.WithContext(ctx)

	for idx, item := range elems {
		idx := idx
		item := item

		g.Go(func() error {
			select {
			case <-gctx.Done():
				return gctx.Err()
			default:
			}

			up := uploader.NewUploader(u.opts.Client).
				WithPartSize(MaxPartSize).
				WithThreads(u.opts.Threads).
				WithProgress(&wrapProcess{
					elem:    item,
					process: u.opts.Progress,
				})

			f, err := up.Upload(gctx, uploader.NewUpload(item.File().Name(), item.File(), item.File().Size()))
			if err != nil {
				return errors.Wrapf(err, "upload file %s", item.File().Name())
			}

			if _, err = item.File().Seek(0, io.SeekStart); err != nil {
				return errors.Wrap(err, "seek file")
			}
			mime, err := mimetype.DetectReader(item.File())
			if err != nil {
				return errors.Wrap(err, "detect mime")
			}

			var inputMedia tg.InputMediaClass
			mimeStr := mime.String()

			switch {
			case mediautil.IsImage(mimeStr) && mimeStr != "image/webp":
				photo := &tg.InputMediaUploadedPhoto{
					File: f,
				}
				photo.SetFlags()
				inputMedia = photo
			default:
				doc := &tg.InputMediaUploadedDocument{
					File:     f,
					MimeType: mimeStr,
					Attributes: []tg.DocumentAttributeClass{
						&tg.DocumentAttributeFilename{FileName: item.File().Name()},
					},
				}
				if mediautil.IsVideo(mimeStr) {
					if _, err = item.File().Seek(0, io.SeekStart); err != nil {
						return errors.Wrap(err, "seek file")
					}
					videoAttr := &tg.DocumentAttributeVideo{
						SupportsStreaming: true,
					}
					if vp, ok := item.(VideoMetaProvider); ok {
						if w, h, dur, ok := vp.VideoMeta(); ok {
							videoAttr.W = w
							videoAttr.H = h
							videoAttr.Duration = dur
						}
					}
					if videoAttr.W == 0 {
						if dur, w, h, err := mediautil.GetMP4Info(item.File()); err == nil {
							videoAttr.Duration = float64(dur)
							videoAttr.W = w
							videoAttr.H = h
						}
					}
					doc.Attributes = append(doc.Attributes, videoAttr)
				} else if mediautil.IsAudio(mimeStr) {
					doc.Attributes = append(doc.Attributes, &tg.DocumentAttributeAudio{
						Title: fsutil.GetNameWithoutExt(item.File().Name()),
					})
				}
				if thumb, ok := item.Thumb(); ok {
					if _, err := thumb.Seek(0, io.SeekStart); err == nil {
						thumbBytes, err := io.ReadAll(thumb)
						if err == nil && len(thumbBytes) > 0 {
							thumbFile, err := uploader.NewUploader(u.opts.Client).
								FromBytes(gctx, "thumb.jpg", thumbBytes)
							if err == nil {
								doc.Thumb = thumbFile
							} else {
								color.Yellow("[%s] Upload thumbnail failed: %v", item.File().Name(), err)
							}
						}
					}
				}
				doc.SetFlags()
				inputMedia = doc
			}

			uploadedMedia, err := u.opts.Client.MessagesUploadMedia(gctx, &tg.MessagesUploadMediaRequest{
				Peer:  item.To(),
				Media: inputMedia,
			})
			if err != nil {
				return errors.Wrapf(err, "upload media %s", item.File().Name())
			}

			if docMedia, ok := uploadedMedia.(*tg.MessageMediaDocument); ok {
				if d, ok := docMedia.Document.AsNotEmpty(); ok {
					color.Cyan("[%s] Telegram document uploaded: ID=%d, Thumbs=%d", item.File().Name(), d.ID, len(d.Thumbs))
				}
			}

			convMedia, ok := tmedia.ConvInputMedia(uploadedMedia)
			if !ok {
				return errors.Errorf("can't convert uploaded media to input class for %s", item.File().Name())
			}

			msg, entities := item.Caption()
			single := tg.InputSingleMedia{
				Media:    convMedia,
				RandomID: randID(),
				Message:  msg,
				Entities: entities,
			}
			single.SetFlags()
			results[idx] = single
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}

	var replyTo tg.InputReplyToClass
	if thread := group.Thread(); thread != 0 {
		replyTo = &tg.InputReplyToMessage{ReplyToMsgID: thread}
	}

	req := &tg.MessagesSendMultiMediaRequest{
		Peer:       group.To(),
		ReplyTo:    replyTo,
		MultiMedia: results,
	}
	req.SetFlags()

	if _, err := u.opts.Client.MessagesSendMultiMedia(ctx, req); err != nil {
		return errors.Wrap(err, "send multi media")
	}

	return nil
}
