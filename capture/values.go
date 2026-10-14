// SPDX-License-Identifier: MIT

package capture

// AddressValues are the raw values of an FC01–04 sample, one per address:
// coils and discrete inputs as 0 or 1, registers as unsigned 16-bit words.
// Nothing is decoded beyond that.
type AddressValues struct {
	Function uint8
	Start    uint16
	Values   []uint16
}

// SplitAddresses splits the response of an FC01, 02, 03 or 04 sample into
// one value per requested address. It returns false for other functions, for
// exception responses, and for a response whose size does not match the
// request. Coil and discrete-input bits come least significant bit first: the
// LSB of the first data byte is the first requested address.
func SplitAddresses(s *Sample) (AddressValues, bool) {
	req, resp := s.Request, s.Response
	if len(req) != 5 || len(resp) < 2 || resp[0] != req[0] {
		return AddressValues{}, false
	}
	fc := req[0]
	start := uint16(req[1])<<8 | uint16(req[2])
	qty := int(uint16(req[3])<<8 | uint16(req[4]))
	if qty == 0 || start+uint16(qty-1) < start {
		return AddressValues{}, false
	}
	data := resp[2:]
	if int(resp[1]) != len(data) {
		return AddressValues{}, false
	}
	av := AddressValues{Function: fc, Start: start, Values: make([]uint16, qty)}
	switch fc {
	case 0x01, 0x02:
		if len(data) != (qty+7)/8 {
			return AddressValues{}, false
		}
		for i := range av.Values {
			av.Values[i] = uint16(data[i/8]>>(i%8)) & 1
		}
	case 0x03, 0x04:
		if len(data) != 2*qty {
			return AddressValues{}, false
		}
		for i := range av.Values {
			av.Values[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
		}
	default:
		return AddressValues{}, false
	}
	return av, true
}
