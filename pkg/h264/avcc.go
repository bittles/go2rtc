// Package h264 - AVCC format related functions
package h264

import (
  "github.com/rs/zerolog/log"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/pion/rtp"
)

func RepairAVCC(codec *core.Codec, handler core.HandlerFunc) core.HandlerFunc {
	sps, pps := GetParameterSet(codec.FmtpLine)
	ps := JoinNALU(sps, pps)

    // Prebuilt AVCC AUD (length-prefixed)
	log.Info().Msg("AVCCToCodec injecting AUD")
    aud := []byte{
        0x00, 0x00, 0x00, 0x02, // length = 2
        0x09, 0xF0,             // AUD
    }

    return func(packet *rtp.Packet) {

        // Remove SEI if present
        if NALUType(packet.Payload) == NALUTypeSEI {
            size := int(binary.BigEndian.Uint32(packet.Payload)) + 4
            packet.Payload = packet.Payload[size:]
        }

        naluType := NALUType(packet.Payload)

        // Inject AUD before slices
        if naluType == NALUTypeIFrame || naluType == NALUTypePFrame {
            // Insert AUD
            packet.Payload = append(aud, packet.Payload...)
        }

        // Inject SPS/PPS before IDR
        if naluType == NALUTypeIFrame {
            packet.Payload = Join(ps, packet.Payload)
        }

        handler(packet)
    }
}

func JoinNALU(nalus ...[]byte) (avcc []byte) {
	var i, n int

	for _, nalu := range nalus {
		if i = len(nalu); i > 0 {
			n += 4 + i
		}
	}

	avcc = make([]byte, n)

	n = 0
	for _, nal := range nalus {
		if i = len(nal); i > 0 {
			binary.BigEndian.PutUint32(avcc[n:], uint32(i))
			n += 4 + copy(avcc[n+4:], nal)
		}
	}

	return
}

func SplitNALU(avcc []byte) [][]byte {
	var nals [][]byte
	for {
		// get AVC length
		size := int(binary.BigEndian.Uint32(avcc)) + 4

		// check if multiple items in one packet
		if size < len(avcc) {
			nals = append(nals, avcc[:size])
			avcc = avcc[size:]
		} else {
			nals = append(nals, avcc)
			break
		}
	}
	return nals
}

func NALUTypes(avcc []byte) []byte {
	var types []byte
	for {
		types = append(types, NALUType(avcc))

		size := 4 + int(binary.BigEndian.Uint32(avcc))
		if size < len(avcc) {
			avcc = avcc[size:]
		} else {
			break
		}
	}
	return types
}

func AVCCToCodec(avcc []byte) *core.Codec {
	buf := bytes.NewBufferString("packetization-mode=1")

	for {
		n := len(avcc)
		if n < 4 {
			break
		}

		size := 4 + int(binary.BigEndian.Uint32(avcc))
		if n < size {
			break
		}

		switch NALUType(avcc) {
		case NALUTypeSPS:
			// Force level_idc to 4.1
			if size >= 8 {
					avcc[7] = 0x29
			}
			buf.WriteString(";profile-level-id=")
			buf.WriteString(hex.EncodeToString(avcc[5:8]))
			buf.WriteString(";sprop-parameter-sets=")
			buf.WriteString(base64.StdEncoding.EncodeToString(avcc[4:size]))
		case NALUTypePPS:
			buf.WriteString(",")
			buf.WriteString(base64.StdEncoding.EncodeToString(avcc[4:size]))
		}

		avcc = avcc[size:]
	}

	return &core.Codec{
		Name:        core.CodecH264,
		ClockRate:   90000,
		FmtpLine:    buf.String(),
		PayloadType: core.PayloadTypeRAW,
	}
}
