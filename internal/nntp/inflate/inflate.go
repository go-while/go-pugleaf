// Package inflate decodes a raw DEFLATE stream (RFC 1951) as it arrives on a connection: the
// receiving side of NNTP COMPRESS DEFLATE (RFC 8054) for go-pugleaf's NNTP client.
//
// Ported unchanged from github.com/go-while/nntp-testkit/internal/inflate (same author); that
// package is internal to the kit and production code must not import a test kit.
//
// compress/flate cannot do this: its reader passes decoded bytes on only at an empty stored
// block (the 00 00 ff ff of a sync flush), when its window is full or at the end, so a peer
// that flushes with zlib's Z_PARTIAL_FLUSH (an empty fixed block, as INN's nnrpd does; RFC
// 8054 section 4 recommends it over Z_FULL_FLUSH) is never readable until it sends more. Run
// passes on every decoded byte before it waits for more input, whatever the flush.
package inflate

import (
	"bufio"
	"compress/flate"
	"fmt"
	"io"
)

const (
	window  = 32 << 10 // the largest distance of a back-reference
	pending = 64 << 10 // decoded bytes held before they are passed on without a wait
)

// Run decodes the DEFLATE stream read from src and passes the decoded bytes to out: before
// it waits for input (src.Buffered() is 0), and whenever 64 KiB are held. out must not keep
// the slice. Run returns io.EOF after a final block, the read error of src (io.EOF when the
// connection ends, wherever in the stream), the error of out, or for corrupt input an error
// wrapping flate.CorruptInputError (the input offset). It reads src no further than the
// decoder needs, except to fill its bit buffer from bytes src already holds.
func Run(src *bufio.Reader, out func([]byte) error) error {
	d := &decoder{src: src, out: out}
	err := d.run()
	if ferr := d.flush(); ferr != nil && err == io.EOF {
		err = ferr
	}
	return err
}

type decoder struct {
	src  *bufio.Reader
	out  func([]byte) error
	bits uint64 // input bits not yet used, the next one lowest
	nb   uint   // number of bits in bits
	off  int64  // input bytes read
	hist []byte // decoded: at least the last 32 KiB passed on, then the bytes not yet passed on
	sent int    // hist[:sent] was passed on
	oerr error  // the error of out: it is not called again
}

// corrupt is the error for invalid input.
func (d *decoder) corrupt(why string) error {
	return fmt.Errorf("corrupt compressed stream (%s): %w", why, flate.CorruptInputError(d.off))
}

// flush passes the decoded bytes on and trims the history to the window. Once out failed it
// returns that error without calling out again.
func (d *decoder) flush() error {
	if d.oerr != nil {
		return d.oerr
	}
	if d.sent < len(d.hist) {
		if err := d.out(d.hist[d.sent:]); err != nil {
			d.oerr = err
			return err
		}
		d.sent = len(d.hist)
	}
	if len(d.hist) >= 2*window {
		n := copy(d.hist, d.hist[len(d.hist)-window:])
		d.hist, d.sent = d.hist[:n], n
	}
	return nil
}

// fill makes at least n bits available: it takes what src holds without waiting, then waits
// for more only after passing on the decoded bytes.
func (d *decoder) fill(n uint) error {
	for d.nb <= 56 && d.src.Buffered() > 0 {
		c, _ := d.src.ReadByte()
		d.add(c)
	}
	for d.nb < n {
		if err := d.flush(); err != nil {
			return err
		}
		c, err := d.src.ReadByte()
		if err != nil {
			return err
		}
		d.add(c)
	}
	return nil
}

func (d *decoder) add(c byte) {
	d.bits |= uint64(c) << d.nb
	d.nb += 8
	d.off++
}

// get returns the next n bits (n <= 16), the first one lowest.
func (d *decoder) get(n uint) (int, error) {
	if d.nb < n {
		if err := d.fill(n); err != nil {
			return 0, err
		}
	}
	v := int(d.bits & (1<<n - 1))
	d.bits >>= n
	d.nb -= n
	return v, nil
}

func (d *decoder) run() error {
	for {
		final, err := d.get(1)
		if err != nil {
			return err
		}
		typ, err := d.get(2)
		if err != nil {
			return err
		}
		switch typ {
		case 0:
			err = d.stored()
		case 1:
			err = d.codes(&fixedLit, &fixedDist)
		case 2:
			err = d.dynamic()
		default:
			err = d.corrupt("invalid block type 3")
		}
		if err != nil {
			return err
		}
		if final == 1 {
			return io.EOF
		}
		if len(d.hist)-d.sent >= pending {
			if err := d.flush(); err != nil {
				return err
			}
		}
	}
}

// stored copies a stored block: LEN, NLEN (its complement), LEN bytes, byte-aligned.
func (d *decoder) stored() error {
	d.bits >>= d.nb % 8
	d.nb -= d.nb % 8
	var hdr [4]int
	for i := range hdr {
		v, err := d.get(8)
		if err != nil {
			return err
		}
		hdr[i] = v
	}
	n, nn := hdr[0]|hdr[1]<<8, hdr[2]|hdr[3]<<8
	if n != ^nn&0xffff {
		return d.corrupt("stored block length does not match its complement")
	}
	for ; n > 0 && d.nb >= 8; n-- { // bytes already in the bit buffer
		d.hist = append(d.hist, byte(d.bits))
		d.bits >>= 8
		d.nb -= 8
	}
	for n > 0 {
		if len(d.hist)-d.sent >= pending || d.src.Buffered() == 0 {
			if err := d.flush(); err != nil {
				return err
			}
		}
		k := min(n, max(d.src.Buffered(), 1))
		d.hist = append(d.hist, make([]byte, k)...)
		got, err := io.ReadFull(d.src, d.hist[len(d.hist)-k:])
		d.off += int64(got)
		if err != nil {
			d.hist = d.hist[:len(d.hist)-k+got]
			return err
		}
		n -= k
	}
	return nil
}

// fastBits is the number of input bits the lookup table of a code resolves at once.
const fastBits = 9

// huffman is a canonical Huffman code: count[l] codes of length l, symbol by code order, and
// fast, indexed by the next fastBits input bits: symbol<<4 | length of a code that short, else 0.
type huffman struct {
	count  [16]int
	symbol []int
	fast   [1 << fastBits]uint32
}

// build makes h from the code lengths; it returns 0 for a complete code, < 0 for an
// over-subscribed one and > 0 for an incomplete one.
func (h *huffman) build(lengths []int) int {
	h.count = [16]int{}
	for _, l := range lengths {
		h.count[l]++
	}
	if h.count[0] == len(lengths) {
		return 0 // no codes: complete, but decoding fails
	}
	left := 1
	for l := 1; l <= 15; l++ {
		left <<= 1
		left -= h.count[l]
		if left < 0 {
			return left
		}
	}
	var offs [16]int
	for l := 1; l < 15; l++ {
		offs[l+1] = offs[l] + h.count[l]
	}
	h.symbol = make([]int, len(lengths))
	for sym, l := range lengths {
		if l != 0 {
			h.symbol[offs[l]] = sym
			offs[l]++
		}
	}
	h.fast = [1 << fastBits]uint32{}
	code, i := 0, 0
	for l := 1; l <= fastBits; l++ {
		for range h.count[l] {
			rev := 0 // the code as it arrives: its first bit lowest
			for b := 0; b < l; b++ {
				rev = rev<<1 | (code>>b)&1
			}
			for f := rev; f < 1<<fastBits; f += 1 << l {
				h.fast[f] = uint32(h.symbol[i]<<4 | l)
			}
			code++
			i++
		}
		code <<= 1
	}
	return left
}

// decode reads one symbol: from the lookup table when enough bits are at hand, else bit by
// bit up to the length of its code, so it never waits for more input than the code needs.
func (d *decoder) decode(h *huffman) (int, error) {
	if d.nb < fastBits && d.src.Buffered() > 0 {
		_ = d.fill(0) // what src holds, without waiting
	}
	if d.nb >= fastBits {
		if e := h.fast[d.bits&(1<<fastBits-1)]; e != 0 {
			l := uint(e & 15)
			d.bits >>= l
			d.nb -= l
			return int(e >> 4), nil
		}
	}
	code, first, index := 0, 0, 0
	for l := uint(1); l <= 15; l++ {
		if d.nb < l {
			if err := d.fill(l); err != nil {
				return 0, err
			}
		}
		code |= int(d.bits>>(l-1)) & 1
		count := h.count[l]
		if code-count < first {
			d.bits >>= l
			d.nb -= l
			return h.symbol[index+code-first], nil
		}
		index += count
		first += count
		first <<= 1
		code <<= 1
	}
	return 0, d.corrupt("invalid Huffman code")
}

var (
	lbase = [29]int{3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 15, 17, 19, 23, 27, 31, 35, 43, 51, 59, 67, 83, 99, 115, 131, 163, 195, 227, 258}
	lext  = [29]uint{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5, 0}
	dbase = [30]int{1, 2, 3, 4, 5, 7, 9, 13, 17, 25, 33, 49, 65, 97, 129, 193, 257, 385, 513, 769, 1025, 1537, 2049, 3073, 4097, 6145, 8193, 12289, 16385, 24577}
	dext  = [30]uint{0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13, 13}

	fixedLit, fixedDist huffman
)

func init() {
	var l [288]int
	for i := range l {
		switch {
		case i < 144:
			l[i] = 8
		case i < 256:
			l[i] = 9
		case i < 280:
			l[i] = 7
		default:
			l[i] = 8
		}
	}
	fixedLit.build(l[:])
	var dl [30]int
	for i := range dl {
		dl[i] = 5
	}
	fixedDist.build(dl[:])
}

// codes decodes the literals and back-references of a block up to its end-of-block code.
func (d *decoder) codes(lit, dist *huffman) error {
	for {
		sym, err := d.decode(lit)
		if err != nil {
			return err
		}
		switch {
		case sym < 256:
			d.hist = append(d.hist, byte(sym))
		case sym == 256:
			return nil
		default:
			sym -= 257
			if sym >= 29 {
				return d.corrupt("invalid length code")
			}
			extra, err := d.get(lext[sym])
			if err != nil {
				return err
			}
			n := lbase[sym] + extra
			ds, err := d.decode(dist)
			if err != nil {
				return err
			}
			if ds >= 30 {
				return d.corrupt("invalid distance code")
			}
			extra, err = d.get(dext[ds])
			if err != nil {
				return err
			}
			back := dbase[ds] + extra
			if back > len(d.hist) {
				return d.corrupt("distance too far back")
			}
			for ; n > 0; n-- {
				d.hist = append(d.hist, d.hist[len(d.hist)-back])
			}
		}
		if len(d.hist)-d.sent >= pending {
			if err := d.flush(); err != nil {
				return err
			}
		}
	}
}

// order is the order of the code length code lengths of a dynamic block.
var order = [19]int{16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15}

// dynamic reads the code tables of a dynamic block and decodes it.
func (d *decoder) dynamic() error {
	var hdr [3]int
	for i, n := range []uint{5, 5, 4} {
		v, err := d.get(n)
		if err != nil {
			return err
		}
		hdr[i] = v
	}
	nlen, ndist, ncode := hdr[0]+257, hdr[1]+1, hdr[2]+4
	if nlen > 286 || ndist > 30 {
		return d.corrupt("too many length or distance codes")
	}
	var cl [19]int
	for i := 0; i < ncode; i++ {
		v, err := d.get(3)
		if err != nil {
			return err
		}
		cl[order[i]] = v
	}
	var lencode huffman
	if lencode.build(cl[:]) != 0 {
		return d.corrupt("incomplete code length code")
	}
	lengths := make([]int, nlen+ndist)
	for i := 0; i < nlen+ndist; {
		sym, err := d.decode(&lencode)
		if err != nil {
			return err
		}
		if sym < 16 {
			lengths[i] = sym
			i++
			continue
		}
		val, rep := 0, 0
		switch sym {
		case 16:
			if i == 0 {
				return d.corrupt("repeat with no first length")
			}
			val = lengths[i-1]
			rep, err = d.get(2)
			rep += 3
		case 17:
			rep, err = d.get(3)
			rep += 3
		default:
			rep, err = d.get(7)
			rep += 11
		}
		if err != nil {
			return err
		}
		if i+rep > nlen+ndist {
			return d.corrupt("too many code lengths")
		}
		for ; rep > 0; rep-- {
			lengths[i] = val
			i++
		}
	}
	if lengths[256] == 0 {
		return d.corrupt("no end-of-block code")
	}
	var lit, dist huffman
	if left := lit.build(lengths[:nlen]); left < 0 || (left > 0 && nlen-lit.count[0] != 1) {
		return d.corrupt("invalid literal/length code lengths")
	}
	if left := dist.build(lengths[nlen:]); left < 0 || (left > 0 && ndist-dist.count[0] != 1) {
		return d.corrupt("invalid distance code lengths")
	}
	return d.codes(&lit, &dist)
}
