package connectorhost

import (
	"errors"
	"fmt"
)

// Binary-format section ids checkDeclaredSizes walks.
const (
	sectionCustom = 0
	sectionType   = 1
	sectionImport = 2
	sectionFunc   = 3
	sectionTable  = 4
	sectionMemory = 5
	sectionGlobal = 6
	sectionExport = 7
	sectionStart  = 8
	sectionElem   = 9
	sectionCode   = 10
	sectionData   = 11
)

// limitsMax is a limits flag bit: a maximum follows the minimum.
const limitsMax = 1

// Subsection ids of the "name" custom section.
const (
	nameModule   = 0
	nameFunction = 1
	nameLocal    = 2
)

// vectorSections are the sections that open with a vector count their wazero
// decoder sizes a slice from: type, import, function, table, memory, global,
// export, element, code and data.
var vectorSections = [...]bool{
	sectionType: true, sectionImport: true, sectionFunc: true, sectionTable: true,
	sectionMemory: true, sectionGlobal: true, sectionExport: true,
	sectionElem: true, sectionCode: true, sectionData: true,
}

// reader reads a wasm binary's bytes and LEB128 u32s. A read past the end, or a
// count the bytes left cannot hold, panics with a scanError that
// checkDeclaredSizes recovers, so the scan reads straight through.
type reader struct{ b []byte }

type scanError struct{ error }

func (r *reader) fail(err error) { panic(scanError{err}) }

// skipLEB consumes one LEB128 integer of any length and signedness.
func (r *reader) skipLEB() {
	for r.byte()&0x80 != 0 {
	}
}

func (r *reader) byte() byte {
	if len(r.b) == 0 {
		r.fail(errors.New("malformed binary: unexpected end"))
	}
	c := r.b[0]
	r.b = r.b[1:]
	return c
}

func (r *reader) u32() uint32 {
	var v uint64
	for shift := uint(0); shift < 35; shift += 7 {
		c := r.byte()
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return uint32(v)
		}
	}
	r.fail(errors.New("malformed binary: u32 over five bytes"))
	return 0
}

// count reads a vector length. Every entry takes a byte at least, so a length
// over the bytes left is a lie.
func (r *reader) count() uint32 {
	n := r.u32()
	if uint64(n) > uint64(len(r.b)) {
		r.fail(fmt.Errorf("%w: a vector declares %d entries in %d bytes", ErrDeclaredSize, n, len(r.b)))
	}
	return n
}

// sub consumes the next n bytes and returns a reader over them.
func (r *reader) sub(n uint32) *reader {
	if uint64(n) > uint64(len(r.b)) {
		r.fail(fmt.Errorf("malformed binary: %d bytes declared, %d left", n, len(r.b)))
	}
	s := &reader{r.b[:n]}
	r.b = r.b[n:]
	return s
}

// bytes consumes a length-prefixed byte string: a name, or a type's value types.
func (r *reader) bytes() []byte { return r.sub(r.u32()).b }

// limits consumes a limits (the flag, then the minimum, then a maximum if the
// flag says so) and returns the minimum.
func (r *reader) limits() uint32 {
	flag := r.byte()
	minimum := r.u32()
	if flag&limitsMax != 0 {
		r.u32()
	}
	return minimum
}

// numConst consumes the operand of op, one of i32.const, i64.const, f32.const
// and f64.const (0x41 to 0x44).
func (r *reader) numConst(op byte) {
	if op <= 0x42 {
		r.skipLEB()
	} else {
		r.sub(4 * uint32(op-0x42))
	}
}

// constExpr consumes a constant expression, whose operand each opcode sizes.
func (r *reader) constExpr() {
	for {
		switch op := r.byte(); op {
		case 0x0b: // end
			return
		case 0x41, 0x42, 0x43, 0x44: // i32.const, i64.const, f32.const, f64.const
			r.numConst(op)
		case 0x23, 0xd2: // global.get, ref.func
			r.skipLEB()
		case 0xd0: // ref.null
			r.byte()
		case 0xfd: // v128.const
			r.u32()
			r.sub(16)
		default:
			r.fail(fmt.Errorf("malformed binary: unexpected opcode 0x%02x in a constant expression", op))
		}
	}
}

// customSection walks a custom section's name, and for the "name" section its
// subsections, which wazero decodes whatever the host asked it to keep.
func (r *reader) customSection() {
	if string(r.bytes()) != "name" {
		return
	}
	for len(r.b) > 0 {
		id, sub := r.byte(), r.sub(r.u32())
		switch id {
		case nameModule:
			sub.bytes()
		case nameFunction:
			for n := sub.count(); n > 0; n-- {
				sub.u32()
				sub.bytes()
			}
		case nameLocal:
			for n := sub.count(); n > 0; n-- {
				sub.u32()
				for m := sub.count(); m > 0; m-- {
					sub.u32()
					sub.bytes()
				}
			}
		}
		// wazero reads a known subsection past its declared size, so leftover
		// bytes would be read as the next subsection, unseen by this scan.
		if id <= nameLocal && len(sub.b) > 0 {
			r.fail(errors.New("malformed binary: name subsection longer than its entries"))
		}
	}
}

// checkDeclaredSizes refuses a module that declares tables, function frames, or
// vectors beyond what the host will allocate for, reading them off the binary
// before CompileModule. wazero's decoder trusts the guest's counts and sizes: it
// sizes a slice or loops on a count before reading the entries, never bounds a
// read by its section's or body's declared size, and allocates tables in full at
// instantiation. None of that shows on the CompiledModule (imported tables
// cannot link, WASI exporting none). The binary is unvalidated, so the scan fails
// closed: leaving a structural problem for CompileModule to report would let the
// decoder act on the same lie first. It walks only what sizes an allocation (a
// function body for the operand stack it can grow), so the module's semantics
// stay CompileModule's to judge.
func checkDeclaredSizes(wasm []byte) (err error) {
	defer func() {
		if p := recover(); p != nil {
			se, ok := p.(scanError)
			if !ok {
				panic(p)
			}
			err = se.error
		}
	}()
	if len(wasm) < 8 {
		return errors.New("malformed binary: no header")
	}
	var tables, locals uint64
	var mod module
	var imported uint32                           // imported functions come first in the function index space
	for r := (&reader{wasm[8:]}); len(r.b) > 0; { // past the magic and version
		id := r.byte()
		sec := r.sub(r.u32())
		if id == sectionCustom {
			sec.customSection()
			continue
		}
		if id == sectionStart {
			// wazero runs it at instantiation under the caller's ctx, unbudgeted.
			return fmt.Errorf("%w: a start section runs outside the ABI; initialise through _initialize", ErrExport)
		}
		if int(id) >= len(vectorSections) || !vectorSections[id] {
			continue
		}
		n := sec.count()
		switch id {
		case sectionType:
			for ; n > 0; n-- {
				sec.byte() // the func form
				params, results := sec.bytes(), sec.bytes()
				mod.sigs = append(mod.sigs, funcSig{uint64(len(params)), uint64(len(results))})
			}
		case sectionImport:
			for ; n > 0; n-- {
				sec.bytes() // module
				sec.bytes() // name
				switch kind := sec.byte(); kind {
				case 0: // func
					mod.funcs = append(mod.funcs, sec.u32())
				case 1: // table
					sec.byte()
					sec.limits()
				case 2: // memory
					sec.limits()
				case 3: // global
					sec.sub(2)
				default:
					sec.fail(fmt.Errorf("malformed binary: import kind %d", kind))
				}
			}
		case sectionFunc:
			imported = uint32(len(mod.funcs))
			for ; n > 0; n-- {
				mod.funcs = append(mod.funcs, sec.u32())
			}
		case sectionTable:
			for ; n > 0; n-- {
				sec.byte() // the element type
				tables += uint64(sec.limits())
			}
			if tables > maxTableEntries {
				return fmt.Errorf("%w: declared table minimums total %d entries, cap %d", ErrDeclaredSize, tables, maxTableEntries)
			}
		case sectionExport:
			for ; n > 0; n-- {
				sec.bytes()
				sec.byte()
				sec.u32()
			}
		case sectionElem:
			for ; n > 0; n-- {
				// The prefix's bit 0 drops the offset, bit 1 adds a table index to an
				// active segment, bit 2 makes the entries expressions; either of bits
				// 0 and 1 means a ref type or element kind byte follows.
				flags := sec.u32()
				if flags > 7 {
					sec.fail(fmt.Errorf("malformed binary: element prefix %d", flags))
				}
				if flags&1 == 0 {
					if flags&2 != 0 {
						sec.u32()
					}
					sec.constExpr()
				}
				if flags&3 != 0 {
					sec.byte()
				}
				for m := sec.count(); m > 0; m-- {
					if flags&4 != 0 {
						sec.constExpr()
					} else {
						sec.u32()
					}
				}
			}
		case sectionCode:
			for i := range n {
				body := sec.sub(sec.u32())
				var fn uint64
				for k := body.count(); k > 0; k-- {
					fn += uint64(body.u32())
					body.byte() // the value type
				}
				slots := mod.funcSig(body, imported+i).params + fn
				slots += mod.maxStackHeight(body)
				if slots > maxFrameSlots {
					return fmt.Errorf("%w: a function frame takes %d slots (parameters, locals and operand stack), cap %d", ErrDeclaredSize, slots, maxFrameSlots)
				}
				if locals += fn; locals > maxModuleLocals {
					return fmt.Errorf("%w: functions declare over %d locals together, cap %d", ErrDeclaredSize, locals, maxModuleLocals)
				}
			}
		case sectionData:
			for ; n > 0; n-- {
				switch flags := sec.u32(); flags {
				case 0, 2: // active; 2 names the memory
					if flags == 2 {
						sec.u32()
					}
					sec.constExpr()
				case 1: // passive
				default:
					sec.fail(fmt.Errorf("malformed binary: data prefix %d", flags))
				}
				sec.bytes()
			}
		}
	}
	return nil
}

// funcSig is a function type's parameter and result counts. SIMD and multi-value
// are off, so every value takes one interpreter slot.
type funcSig struct{ params, results uint64 }

// module is what the code-section walk needs from the sections before it.
type module struct {
	sigs  []funcSig // the type section
	funcs []uint32  // each function's type index, imports first
}

func (m *module) sig(r *reader, typeIdx uint32) funcSig {
	if uint64(typeIdx) >= uint64(len(m.sigs)) {
		r.fail(errors.New("malformed binary: type index out of range"))
	}
	return m.sigs[typeIdx]
}

func (m *module) funcSig(r *reader, funcIdx uint32) funcSig {
	if uint64(funcIdx) >= uint64(len(m.funcs)) {
		r.fail(errors.New("malformed binary: function index out of range"))
	}
	return m.sig(r, m.funcs[funcIdx])
}

// blockResults reads a block type: 0x40 for no result or one value type.
// Multi-value is off, so a type index (a longer encoding) cannot validate.
func (r *reader) blockResults() uint64 {
	switch b := r.byte(); b {
	case 0x40:
		return 0
	case 0x7f, 0x7e, 0x7d, 0x7c: // i32, i64, f32, f64
		return 1
	default:
		r.fail(fmt.Errorf("malformed binary: block type 0x%02x", b))
		return 0
	}
}

// maxStackHeight walks a function body's instructions and returns the most
// operands its stack holds at once, by the validation rules. After br, br_table,
// return and unreachable the stack is polymorphic; the code there never runs, so
// the height drops to the block's base and pops stop at it. The binary is
// unvalidated, so an opcode this table does not know fails the scan.
func (m *module) maxStackHeight(r *reader) (peak uint64) {
	type block struct{ base, results uint64 }
	var h uint64
	blocks := []block{{}} // the function's own block, closed by the final end
	base := func() uint64 { return blocks[len(blocks)-1].base }
	push := func(n uint64) { h += n; peak = max(peak, h) }
	pop := func(n uint64) { h -= min(n, h-base()) }
	enter := func() { blocks = append(blocks, block{h, r.blockResults()}) }
	for len(blocks) > 0 {
		switch op := r.byte(); {
		case op == 0x00 || op == 0x0f: // unreachable, return
			h = base()
		case op == 0x01: // nop
		case op == 0x02 || op == 0x03: // block, loop
			enter()
		case op == 0x04: // if
			pop(1)
			enter()
		case op == 0x05: // else
			h = base()
		case op == 0x0b: // end
			b := blocks[len(blocks)-1]
			blocks = blocks[:len(blocks)-1]
			h = b.base
			push(b.results)
		case op == 0x0c: // br
			r.u32()
			h = base()
		case op == 0x0d: // br_if
			r.u32()
			pop(1)
		case op == 0x0e: // br_table
			for n := r.count(); n > 0; n-- {
				r.u32()
			}
			r.u32()
			h = base()
		case op == 0x10: // call
			s := m.funcSig(r, r.u32())
			pop(s.params)
			push(s.results)
		case op == 0x11: // call_indirect
			s := m.sig(r, r.u32())
			r.u32()
			pop(1 + s.params)
			push(s.results)
		case op == 0x1a || op == 0x21 || op == 0x24: // drop, local.set, global.set
			if op != 0x1a {
				r.u32()
			}
			pop(1)
		case op == 0x1b: // select
			pop(2)
		case op == 0x20 || op == 0x23: // local.get, global.get
			r.u32()
			push(1)
		case op == 0x22: // local.tee
			r.u32()
		case op >= 0x28 && op <= 0x3e: // loads, then stores
			r.u32()
			r.u32()
			if op >= 0x36 {
				pop(2)
			}
		case op == 0x3f || op == 0x40: // memory.size, memory.grow
			r.byte()
			if op == 0x3f {
				push(1)
			}
		case op >= 0x41 && op <= 0x44: // i32.const, i64.const, f32.const, f64.const
			r.numConst(op)
			push(1)
		case op >= 0x45 && op <= 0xc4: // the numeric instructions
			if numericPopsOne(op) {
				pop(1)
			}
		case op == 0xfc:
			r.bulk(pop)
		default:
			r.fail(fmt.Errorf("malformed binary: unexpected opcode 0x%02x in a function body", op))
		}
	}
	if len(r.b) > 0 {
		r.fail(errors.New("malformed binary: bytes after a function's end"))
	}
	return peak
}

// numericPopsOne reports whether a numeric opcode (0x45 to 0xc4) takes two
// operands and leaves one: every comparison but eqz, and every binary operator.
// The rest, eqz and the unary operators and conversions, leave the height as is.
func numericPopsOne(op byte) bool {
	switch {
	case op >= 0x46 && op <= 0x4f, op >= 0x51 && op <= 0x66, // i32 and i64 comparisons, f32 and f64 comparisons
		op >= 0x6a && op <= 0x78, op >= 0x7c && op <= 0x8a, // i32 and i64 arithmetic
		op >= 0x92 && op <= 0x98, op >= 0xa0 && op <= 0xa6: // f32 and f64 arithmetic
		return true
	}
	return false
}

// bulk reads the 0xfc-prefixed instructions that bulk memory and non-trapping
// float-to-int conversion add; reference types and the table instructions that
// need them are off, so table.init, elem.drop and table.copy fail to validate
// but are read anyway.
func (r *reader) bulk(pop func(uint64)) {
	switch sub := r.u32(); sub {
	case 0, 1, 2, 3, 4, 5, 6, 7: // trunc_sat
	case 8: // memory.init
		r.u32()
		r.byte()
		pop(3)
	case 9, 13: // data.drop, elem.drop
		r.u32()
	case 10: // memory.copy
		r.sub(2)
		pop(3)
	case 11: // memory.fill
		r.byte()
		pop(3)
	case 12, 14: // table.init, table.copy
		r.u32()
		r.u32()
		pop(3)
	default:
		r.fail(fmt.Errorf("malformed binary: unexpected opcode 0xfc %d in a function body", sub))
	}
}
