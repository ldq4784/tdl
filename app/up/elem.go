package up

import (
	"os"
	"path/filepath"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/core/uploader"
)

type iterElem struct {
	file    *uploaderFile
	thumb   *uploaderFile
	to      peers.Peer
	caption *entity.Builder
	thread  int

	asPhoto     bool
	remove      bool
	isTempThumb bool

	videoW   int
	videoH   int
	videoDur float64
}

func (e *iterElem) VideoMeta() (int, int, float64, bool) {
	if e.videoW > 0 && e.videoH > 0 {
		return e.videoW, e.videoH, e.videoDur, true
	}
	return 0, 0, 0, false
}

func (e *iterElem) File() uploader.File {
	return e.file
}

func (e *iterElem) Thumb() (uploader.File, bool) {
	if e.thumb == nil {
		return nil, false
	}
	return e.thumb, true
}

func (e *iterElem) Caption() (string, []tg.MessageEntityClass) {
	return e.caption.Complete()
}

func (e *iterElem) To() tg.InputPeerClass {
	return e.to.InputPeer()
}

func (e *iterElem) Thread() int {
	return e.thread
}

func (e *iterElem) AsPhoto() bool {
	return e.asPhoto
}

type uploaderFile struct {
	*os.File
	size int64
}

func (u *uploaderFile) Name() string {
	return filepath.Base(u.File.Name())
}

func (u *uploaderFile) Size() int64 {
	return u.size
}

type groupElem struct {
	elems  []uploader.Elem
	to     tg.InputPeerClass
	thread int
}

func (g *groupElem) Elems() []uploader.Elem {
	return g.elems
}

func (g *groupElem) To() tg.InputPeerClass {
	return g.to
}

func (g *groupElem) Thread() int {
	return g.thread
}

func (g *groupElem) File() uploader.File {
	if len(g.elems) > 0 {
		return g.elems[0].File()
	}
	return nil
}

func (g *groupElem) Thumb() (uploader.File, bool) {
	if len(g.elems) > 0 {
		return g.elems[0].Thumb()
	}
	return nil, false
}

func (g *groupElem) Caption() (string, []tg.MessageEntityClass) {
	if len(g.elems) > 0 {
		return g.elems[0].Caption()
	}
	return "", nil
}

func (g *groupElem) AsPhoto() bool {
	if len(g.elems) > 0 {
		return g.elems[0].AsPhoto()
	}
	return false
}
