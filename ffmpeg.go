package video

// #cgo pkg-config: libavcodec libavutil libavformat libswscale
// #cgo CFLAGS: -std=c11
/*

#include <libavcodec/avcodec.h>
#include <libavutil/frame.h>
#include <libavutil/pixdesc.h>
#include <libavutil/avutil.h>
#include <libavformat/avformat.h>
#include <libavformat/avio.h>
#include <libswscale/swscale.h>
#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>

extern int readCallBack(void*, uint8_t*, int);
extern int writeCallBack(void*, uint8_t*, int);
extern int64_t seekCallBack(void*, int64_t, int);

static inline AVFormatContext * create_context(AVFormatContext *ctx)
{
	char errstringbuf[1024];
	int err = avformat_open_input(&ctx, NULL, NULL, NULL);
	if (err < 0) {
		av_strerror(err,errstringbuf,1024);
		fprintf(stderr,"%s\n",errstringbuf);
		return NULL;
	}
	err = avformat_find_stream_info(ctx,NULL);
	if (err < 0) {
		av_strerror(err,errstringbuf,1024);
		fprintf(stderr,"%s\n",errstringbuf);
		return NULL;
	}

	return ctx;
}

static inline AVFrame * extract_video_image(AVFormatContext *ctx)
{
	char errstringbuf[1024];
	int err;
	AVCodec * codec = NULL;
	//Get video stream ID
	int strm = av_find_best_stream(ctx, AVMEDIA_TYPE_VIDEO, -1, -1, &codec, 0);

	AVCodecContext * codecCtx = ctx->streams[strm]->codec;
	//Open video decoder
	err = avcodec_open2(codecCtx, codec, NULL);
	if (err < 0) {
		av_strerror(err,errstringbuf,1024);
		fprintf(stderr,"%s\n",errstringbuf);
		return NULL;
	}


	for (;;)
	{
		AVPacket pkt;
		err = av_read_frame(ctx, &pkt);
		if (err < 0) {
			av_strerror(err,errstringbuf,1024);
			fprintf(stderr,"%s\n",errstringbuf);
			return NULL;
		}

		if (pkt.stream_index == strm)
		{
			int got = 0;
			AVFrame * frame = av_frame_alloc();
			err = avcodec_decode_video2(codecCtx, frame, &got, &pkt);
			if (err < 0) {
				av_strerror(err,errstringbuf,1024);
				fprintf(stderr,"%s\n",errstringbuf);
				return NULL;
			}

			if (got) {
				return frame;
			}
			av_frame_free(&frame);
		}
	}
}

static inline AVCodecContext * extract_video(AVFormatContext *ctx)
{
	char errstringbuf[1024];
	int err;
	AVCodec * codec = NULL;
	int strm = av_find_best_stream(ctx, AVMEDIA_TYPE_VIDEO, -1, -1, &codec, 0);

	AVCodecContext * codecCtx = ctx->streams[strm]->codec;
	err = avcodec_open2(codecCtx, codec, NULL);
	if (err < 0) {
		av_strerror(err,errstringbuf,1024);
		fprintf(stderr,"%s\n",errstringbuf);
		return NULL;
	}
	return codecCtx;
}

static inline AVCodecContext * extract_audio(AVFormatContext *ctx)
{
	char errstringbuf[1024];
	int err;
	AVCodec * codec = NULL;
	int strm = av_find_best_stream(ctx, AVMEDIA_TYPE_AUDIO, -1, -1, &codec, 0);
	if (strm < 0) {
		return NULL;
	}
	AVCodecContext * codecCtx = ctx->streams[strm]->codec;
	err = avcodec_open2(codecCtx, codec, NULL);
	if (err < 0) {
		av_strerror(err,errstringbuf,1024);
		fprintf(stderr,"%s\n",errstringbuf);
		return NULL;
	}
	return codecCtx;
}
*/
import "C"
import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"strings"
	"sync"
	"time"
	"unsafe"
)

func init() {
	C.av_register_all()
	C.avcodec_register_all()
}

var (
	IO_BUFFER_SIZE int = 4096

	// Global map of AVIOHandlers
	// One handlers struct per format context. Using ctx pointer address as a
	// key.
	handlersMap = handlerMap{
		m: make(map[uintptr]*avIOHandlers),
	}
)

/////////////////////////////////////
// Functions prototypes for custom IO. Implement necessary prototypes and pass instance pointer to NewAVIOContext.
//
// E.g.:
// 	func gridFsReader() ([]byte, int) {
// 		... implementation ...
//		return data, length
//	}
//
//	avoictx := NewAVIOContext(ctx, &AVIOHandlers{ReadPacket: gridFsReader})
type avIOHandlers struct {
	ReadPacket  func([]byte) (int, error)
	WritePacket func([]byte) (int, error)
	Seek        func(int64, int) (int64, error)
}

type avIOContext struct {
	// avAVIOContext *_Ctype_AVIOContext
	avAVIOContext *C.struct_AVIOContext
	handlerKey    uintptr
}

type handlerMap struct {
	sync.RWMutex
	m map[uintptr]*avIOHandlers
}

func (h *handlerMap) Set(k uintptr, handlers *avIOHandlers) {
	h.Lock()
	h.m[k] = handlers
	h.Unlock()
}

func (h *handlerMap) Delete(k uintptr) {
	h.Lock()
	delete(h.m, k)
	h.Unlock()
}

func (h *handlerMap) Get(k unsafe.Pointer) *avIOHandlers {
	h.RLock()
	handlers, ok := h.m[uintptr(k)]
	h.RUnlock()
	if !ok {
		panic(fmt.Sprintf(
			"No handlers instance found, according pointer: %v",
			k,
		))
	}
	return handlers
}

// AVIOContext constructor. Use it only if You need custom IO behaviour!
func newAVIOContext(ctx *C.AVFormatContext, handlers *avIOHandlers) (*avIOContext, error) {
	this := &avIOContext{}

	buffer := (*C.uchar)(C.av_malloc(C.size_t(IO_BUFFER_SIZE)))

	if buffer == nil {
		return nil, errors.New("unable to allocate buffer")
	}

	// we have to explicitly set it to nil, to force library using default handlers
	var ptrRead, ptrWrite, ptrSeek *[0]byte = nil, nil, nil

	if handlers != nil {
		this.handlerKey = uintptr(unsafe.Pointer(ctx))
		handlersMap.Set(this.handlerKey, handlers)
	}

	if handlers.ReadPacket != nil {
		ptrRead = (*[0]byte)(C.readCallBack)
	}

	if handlers.WritePacket != nil {
		ptrWrite = (*[0]byte)(C.writeCallBack)
	}

	if handlers.Seek != nil {
		ptrSeek = (*[0]byte)(C.seekCallBack)
	}

	if this.avAVIOContext = C.avio_alloc_context(buffer, C.int(IO_BUFFER_SIZE), 0, unsafe.Pointer(ctx), ptrRead, ptrWrite, ptrSeek); this.avAVIOContext == nil {
		return nil, errors.New("unable to initialize avio context")
	}

	return this, nil
}

func (this *avIOContext) Free() {
	handlersMap.Delete(this.handlerKey)
}

//export readCallBack
func readCallBack(opaque unsafe.Pointer, buf *C.uint8_t, buf_size C.int) C.int {
	handlers := handlersMap.Get(opaque)
	if handlers.ReadPacket == nil {
		panic("No reader handler initialized")
	}
	s := (*[1 << 30]byte)(unsafe.Pointer(buf))[:buf_size:buf_size]
	n, err := handlers.ReadPacket(s)
	if err != nil {
		return -1
	}
	return C.int(n)
}

//export writeCallBack
func writeCallBack(opaque unsafe.Pointer, buf *C.uint8_t, buf_size C.int) C.int {
	handlers := handlersMap.Get(opaque)
	if handlers.WritePacket == nil {
		panic("No writer handler initialized.")
	}

	n, err := handlers.WritePacket(C.GoBytes(unsafe.Pointer(buf), buf_size))
	if err != nil {
		return -1
	}
	return C.int(n)
}

//export seekCallBack
func seekCallBack(opaque unsafe.Pointer, offset C.int64_t, whence C.int) C.int64_t {
	handlers := handlersMap.Get(opaque)
	if handlers.Seek == nil {
		panic("No seek handler initialized.")
	}

	n, err := handlers.Seek(int64(offset), int(whence))
	if err != nil {
		return -1
	}
	return C.int64_t(n)
}

/////////////////////////////////////

//Uses CGo FFmpeg binding to extract video frame
func Decode(data []byte) (image.Image, error) {
	ctx := C.avformat_alloc_context()
	r := bytes.NewReader(data)
	avioCtx, err := newAVIOContext(ctx, &avIOHandlers{ReadPacket: r.Read, Seek: r.Seek})
	if err != nil {
		panic(err)
	}
	ctx.pb = avioCtx.avAVIOContext
	if ctx = C.create_context(ctx); ctx == nil {
		avioCtx.Free()
		return nil, errors.New("Failed to initialize AVFormatContext")
	}
	f := C.extract_video_image(ctx)
	if f == nil {
		return nil, errors.New("Failed to Get AVCodecContext")
	}

	if C.GoString(C.av_get_pix_fmt_name(int32(f.format))) != "yuv420p" {
		return nil, errors.New("Didn't get format: " + image.YCbCrSubsampleRatio420.String() + "instead got: " + C.GoString(C.av_get_pix_fmt_name(int32(f.format))))
	}
	y := C.GoBytes(unsafe.Pointer(f.data[0]), f.linesize[0]*f.height)
	u := C.GoBytes(unsafe.Pointer(f.data[1]), f.linesize[0]*f.height/4)
	v := C.GoBytes(unsafe.Pointer(f.data[2]), f.linesize[0]*f.height/4)

	return &image.YCbCr{Y: y,
		Cb:             u,
		Cr:             v,
		YStride:        int(f.linesize[0]),
		CStride:        int(f.linesize[0]) / 2,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rectangle{Min: image.Point{X: 0, Y: 0}, Max: image.Point{X: int(f.width), Y: int(f.height) / 2 * 2}}}, nil
}

//Uses CGo FFmpeg binding to find video config
func DecodeConfig(data []byte) (image.Config, error) {
	ctx := C.avformat_alloc_context()
	r := bytes.NewReader(data)
	avioCtx, err := newAVIOContext(ctx, &avIOHandlers{ReadPacket: r.Read, Seek: r.Seek})
	if err != nil {
		panic(err)
	}
	ctx.pb = avioCtx.avAVIOContext
	if ctx = C.create_context(ctx); ctx == nil {
		avioCtx.Free()
		return image.Config{}, errors.New("Failed to initialize AVFormatContext")
	}
	f := C.extract_video(ctx)
	if f == nil {
		return image.Config{}, errors.New("Failed to decode")
	}
	if strings.Contains(C.GoString(C.av_get_pix_fmt_name(int32(f.pix_fmt))), "yuv") {
		return image.Config{ColorModel: color.YCbCrModel,
			Width:  int(f.width),
			Height: int(f.height)}, nil
	} else {
		return image.Config{ColorModel: color.RGBAModel,
			Width:  int(f.width),
			Height: int(f.height)}, nil
	}
}

func DecodeAVFormatDetail(data []byte) (audio, video string, err error) {
	ctx := C.avformat_alloc_context()
	r := bytes.NewReader(data)
	avioCtx, err := newAVIOContext(ctx, &avIOHandlers{ReadPacket: r.Read, Seek: r.Seek})
	if err != nil {
		panic(err)
	}
	ctx.pb = avioCtx.avAVIOContext
	if ctx = C.create_context(ctx); ctx == nil {
		avioCtx.Free()
		err = errors.New("Failed to initialize AVFormatContext")
		return
	}
	f := C.extract_video(ctx)
	if f == nil {
		err = errors.New("Failed to decode video stream")
		return
	}
	video = C.GoString(f.codec.long_name)

	f = C.extract_audio(ctx)
	if f == nil {
		err = errors.New("Failed to decode audio stream")
		return
	}
	audio = C.GoString(f.codec.long_name)
	return
}

func DecodeAVFormat(data []byte) (audio, video string, err error) {
	ctx := C.avformat_alloc_context()
	r := bytes.NewReader(data)
	avioCtx, err := newAVIOContext(ctx, &avIOHandlers{ReadPacket: r.Read, Seek: r.Seek})
	if err != nil {
		panic(err)
	}
	ctx.pb = avioCtx.avAVIOContext
	if ctx = C.create_context(ctx); ctx == nil {
		avioCtx.Free()
		err = errors.New("Failed to initialize AVFormatContext")
		return
	}
	f := C.extract_video(ctx)
	if f == nil {
		err = errors.New("Failed to decode video stream")
		return
	}
	video = C.GoString(f.codec.name)

	f = C.extract_audio(ctx)
	if f == nil {
		err = errors.New("Failed to decode audio stream")
		return
	}
	audio = C.GoString(f.codec.name)
	return
}

func DecodeLength(r io.ReadSeeker) (time.Duration, error) {
	ctx := C.avformat_alloc_context()
	avioCtx, err := newAVIOContext(ctx, &avIOHandlers{ReadPacket: r.Read, Seek: r.Seek})
	if err != nil {
		panic(err)
	}
	ctx.pb = avioCtx.avAVIOContext
	if ctx = C.create_context(ctx); ctx == nil {
		avioCtx.Free()
		return 0, errors.New("Failed to initialize AVFormatContext")
	}
	return time.Duration(ctx.duration * 1000), nil
}
