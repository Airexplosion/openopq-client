package protocol

import "encoding/binary"

// The legacy profile service is still distinct from the NT approval service.
// This is a minimal JCE writer for its v3 RequestPacket, not a general decoder.
func jceNumber(tag byte, n uint64) []byte {
	if n == 0 {
		return []byte{tag<<4 | 12}
	}
	if n <= 127 {
		return []byte{tag << 4, byte(n)}
	}
	if n <= 32767 {
		return []byte{tag<<4 | 1, byte(n >> 8), byte(n)}
	}
	if n <= 2147483647 {
		b := make([]byte, 5)
		b[0] = tag<<4 | 2
		binary.BigEndian.PutUint32(b[1:], uint32(n))
		return b
	}
	b := make([]byte, 9)
	b[0] = tag<<4 | 3
	binary.BigEndian.PutUint64(b[1:], n)
	return b
}
func jceString(tag byte, s string) []byte {
	if len(s) < 256 {
		return append([]byte{tag<<4 | 6, byte(len(s))}, []byte(s)...)
	}
	b := make([]byte, 5)
	b[0] = tag<<4 | 7
	binary.BigEndian.PutUint32(b[1:], uint32(len(s)))
	return append(b, []byte(s)...)
}
func jceBytes(tag byte, b []byte) []byte {
	out := []byte{tag<<4 | 13, 0}
	out = append(out, jceNumber(0, uint64(len(b)))...)
	return append(out, b...)
}
func BuildFriendRemark(uin uint32, remark string) []byte {
	request := []byte{10}
	request = append(request, jceNumber(0, uint64(uin))...)
	request = append(request, jceString(1, remark)...)
	request = append(request, 11)
	args := []byte{8}
	args = append(args, jceNumber(0, 1)...)
	args = append(args, jceString(0, "req")...)
	args = append(args, jceBytes(1, request)...)
	out := jceNumber(1, 3)
	for _, tag := range []byte{2, 3, 4} {
		out = append(out, jceNumber(tag, 0)...)
	}
	out = append(out, jceString(5, "KQQ.ProfileService.ProfileServantObj")...)
	out = append(out, jceString(6, "ChangeFriendName")...)
	out = append(out, jceBytes(7, args)...)
	out = append(out, jceNumber(8, 0)...)
	out = append(out, 0x98, 0x0c, 0xa8, 0x0c)
	return out
}
