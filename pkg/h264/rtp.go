package h264

import (
	"github.com/rs/zerolog/log"
	"encoding/binary"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264/annexb"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)
panic("RTPDepay called")
const RTPPacketVersionAVC = 0

const PSMaxSize = 128 // the biggest SPS I've seen is 48 (EZVIZ CS-CV210)

func patchSPS(nal []byte) []byte {
    if len(nal) >= 4 {
        nal[3] = 0x29 // Level 4.1
    }
    return nal
}

func RTPDepay(codec *core.Codec, handler core.HandlerFunc) core.HandlerFunc {

	depack := &codecs.H264Packet{IsAVC: true}
	sps, pps := GetParameterSet(codec.FmtpLine)

	// Patch level in SDP SPS
	// outputting fmptline to log
	//log.Printf("[RTP] Fmtpline: %s", codec.FmtpLine)
	log.Info().Str("fmtpline", codec.FmtpLine).Msg("[rtp] ouput from FmtpLine")
	//fmt.Println(codec.FmtpLine)
	if len(sps) >= 4 {
			sps[3] = 0x29 // Level 4.1
	}

	ps := JoinNALU(sps, pps)


	buf := make([]byte, 0, 512*1024) // 512K

	return func(packet *rtp.Packet) {
		//log.Printf("[RTP] codec: %s, nalu: %2d, size: %6d, ts: %10d, pt: %2d, ssrc: %d, seq: %d, %v", codec.Name, packet.Payload[0]&0x1F, len(packet.Payload), packet.Timestamp, packet.PayloadType, packet.SSRC, packet.SequenceNumber, packet.Marker)

		payload, err := depack.Unmarshal(packet.Payload)
		if len(payload) == 0 || err != nil {
			return
		}

		// Memory overflow protection. Can happen if we miss a lot of packets with the marker.
		// https://github.com/AlexxIT/go2rtc/issues/675
		if len(buf) > 5*1024*1024 {
			buf = buf[: 0 : 512*1024]
		}

		// Fix TP-Link Tapo TC70: sends SPS and PPS with packet.Marker = true
		// Reolink Duo 2: sends SPS with Marker and PPS without
		if packet.Marker && len(payload) < PSMaxSize {
			switch NALUType(payload) {
			case NALUTypeSPS, NALUTypePPS:
				buf = append(buf, payload...)
				return
			case NALUTypeSEI:
				// RtspServer https://github.com/AlexxIT/go2rtc/issues/244
				// sends, marked SPS, marked PPS, marked SEI, marked IFrame
				return
			}
		}

		if len(buf) == 0 {
			for {
				// Amcrest IP4M-1051: 9, 7, 8, 6, 28...
				// Amcrest IP4M-1051: 9, 6, 1
				switch NALUType(payload) {
				case NALUTypeIFrame:
					// fix IFrame without SPS,PPS
					buf = append(buf, ps...)
				case NALUTypeSEI, NALUTypeAUD:
					// fix ffmpeg with transcoding first frame
					i := int(4 + binary.BigEndian.Uint32(payload))

					// check if only one NAL (fix ffmpeg transcoding for Reolink RLC-510A)
					if i == len(payload) {
						return
					}

					payload = payload[i:]
					continue
				case NALUTypePFrame, NALUTypeSPS, NALUTypePPS: // pass
				default:
					return // skip any unknown NAL unit type
				}
				break
			}
		}

		// collect all NALs for Access Unit
		if !packet.Marker {
			buf = append(buf, payload...)
			return
		}

		if len(buf) > 0 {
			payload = append(buf, payload...)
			buf = buf[:0]
		}

		// should not be that huge SPS
		if NALUType(payload) == NALUTypeSPS && binary.BigEndian.Uint32(payload) >= PSMaxSize {
			// some Chinese buggy cameras have a single packet with SPS+PPS+IFrame separated by 00 00 00 01
			// https://github.com/AlexxIT/WebRTC/issues/391
			// https://github.com/AlexxIT/WebRTC/issues/392
			payload = annexb.FixAnnexBInAVCC(payload)
		}

		//log.Printf("[AVC] %v, len: %d, ts: %10d, seq: %d", NALUTypes(payload), len(payload), packet.Timestamp, packet.SequenceNumber)
		// ---- SPS LEVEL PATCH START ----

		offset := 0
		for offset+4 <= len(payload) {
				size := int(binary.BigEndian.Uint32(payload[offset:]))
				offset += 4

				if offset+size > len(payload) {
						break
				}

				nal := payload[offset : offset+size]

				if len(nal) >= 4 && (nal[0]&0x1F) == NALUTypeSPS {
						nal[3] = 0x29 // Force level 4.1
				}

				offset += size
		}

		// ---- SPS LEVEL PATCH END ----



		clone := *packet
		clone.Version = RTPPacketVersionAVC
		clone.Payload = payload
		handler(&clone)
	}
}

func RTPPay(mtu uint16, handler core.HandlerFunc) core.HandlerFunc {
	if mtu == 0 {
		mtu = 1472
	}

	payloader := &Payloader{IsAVC: true}
	sequencer := rtp.NewRandomSequencer()
	mtu -= 12 // rtp.Header size

	return func(packet *rtp.Packet) {
		if packet.Version != RTPPacketVersionAVC {
			handler(packet)
			return
		}

		payloads := payloader.Payload(mtu, packet.Payload)
		last := len(payloads) - 1
		for i, payload := range payloads {
			clone := rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					Marker:         i == last,
					SequenceNumber: sequencer.NextSequenceNumber(),
					Timestamp:      packet.Timestamp,
				},
				Payload: payload,
			}
			handler(&clone)
		}
	}
}
