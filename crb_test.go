// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, the go-tpm2/crb authors. All rights reserved.

package crb

import (
	"bytes"
	"testing"

	"github.com/go-tpm2/common"
)

// fakeCRB is a common.Regs that simulates a single-locality CRB control
// area plus its data buffer, modelling the subset of the PTP state
// machine the driver drives. A flat register map at offsets < dataBase
// holds the little-endian control registers; the data buffer follows.
//
// Behaviour is steered by the knobs below so each test can force a
// specific branch of Send / Open. By default it plays a clean happy
// path: locality is granted on request, command-ready is granted, and on
// CTRL_START.start it "runs" a canned response and clears start.
type fakeCRB struct {
	reg  map[uint32]uint32 // little-endian control registers
	data []byte            // command/response data buffer

	// canned response the simulated TPM emits when a command is started.
	response []byte

	// lastCmd captures the command the driver wrote into the shared data
	// buffer at the instant CTRL_START.start fires, before the canned
	// response overwrites it (the command and response share one buffer).
	lastCmd []byte

	// knobs
	ifaceType    uint32 // INTERFACE_ID InterfaceType nibble (default CRB)
	neverValid   bool   // LOC_STATE.tpmRegValidSts never sets
	neverGranted bool   // locality never granted
	neverReady   bool   // command-ready never reached
	readyError   bool   // CTRL_STS.Error during requestReady
	neverDone    bool   // CTRL_START.start never clears
	stsErrorDone bool   // CTRL_STS.Error after completion

	// trace of CTRL_CANCEL writes, to assert the cancel handshake.
	cancelWrites []uint32

	// started becomes true once CTRL_START.start has fired and the
	// command has "run"; stsErrorDone only surfaces after this so the
	// error is seen at the post-completion check, not during requestReady.
	started bool
}

const dataBase = regData

func newFakeCRB() *fakeCRB {
	f := &fakeCRB{
		reg:       make(map[uint32]uint32),
		data:      make([]byte, dataBase+bufSize),
		ifaceType: ifaceTypeCRB,
		response: common.BuildCommand(
			uint16(common.TagNoSessions), uint32(common.RCSuccess),
			[]byte{0xDE, 0xAD},
		),
	}
	return f
}

// align rounds an offset down to its dword for the register map.
func (f *fakeCRB) Read32(off uint32) uint32 {
	switch off {
	case regInterfaceID:
		return f.ifaceType & ifaceTypeMask
	case regLocState:
		v := uint32(0)
		if !f.neverValid {
			v |= locStateValid
		}
		// locAssigned reflects whether the driver has been granted and
		// not yet relinquished.
		if f.reg[regLocState]&locStateLocAssigned != 0 {
			v |= locStateLocAssigned
		}
		return v
	case regLocSts:
		if f.neverGranted {
			return 0
		}
		return locStsGranted
	case regCtrlSts:
		var v uint32
		if f.readyError || (f.stsErrorDone && f.started) {
			v |= ctrlStsError
		}
		// Report Idle until command-ready has been granted, unless we are
		// forcing the never-ready branch.
		if f.neverReady || f.reg[regCtrlReq]&0x80000000 == 0 {
			// bit31 of our shadow CTRL_REQ marks "ready was granted".
			v |= ctrlStsIdle
		}
		return v
	case regCtrlReq:
		// cmdReady self-clears once ready has been granted.
		return f.reg[regCtrlReq] & ^uint32(0x80000000|ctrlReqCmdReady)
	case regCtrlStart:
		return f.reg[regCtrlStart]
	default:
		return f.reg[off]
	}
}

func (f *fakeCRB) Write32(off uint32, v uint32) {
	switch off {
	case regLocCtrl:
		if v&locCtrlRequestAccess != 0 && !f.neverGranted {
			f.reg[regLocState] |= locStateLocAssigned
		}
		if v&locCtrlRelinquish != 0 {
			f.reg[regLocState] &^= locStateLocAssigned
		}
	case regCtrlReq:
		if v&ctrlReqCmdReady != 0 && !f.neverReady && !f.readyError {
			// Mark ready granted via our shadow bit; cmdReady self-clears.
			f.reg[regCtrlReq] = 0x80000000
		} else if v&ctrlReqCmdReady != 0 {
			f.reg[regCtrlReq] = ctrlReqCmdReady // stays set => never ready
		}
		if v&ctrlReqGoIdle != 0 {
			f.reg[regCtrlReq] = 0 // back to Idle
		}
	case regCtrlStart:
		f.reg[regCtrlStart] = v
		if v&ctrlStart != 0 && !f.neverDone {
			// Snapshot the command the driver placed in the shared
			// buffer, then "run" it: overwrite the buffer with the canned
			// response and clear start to signal completion.
			f.lastCmd = append([]byte(nil), f.data[dataBase:]...)
			copy(f.data[dataBase:], f.response)
			f.reg[regCtrlStart] = 0
			f.started = true
		}
	case regCtrlCancel:
		f.cancelWrites = append(f.cancelWrites, v)
	default:
		f.reg[off] = v
	}
}

func (f *fakeCRB) Read8(off uint32) uint8 { return f.data[off] }

func (f *fakeCRB) Write8(off uint32, v uint8) { f.data[off] = v }

// withShortSpin shrinks maxSpin for the duration of fn so timeout
// branches resolve quickly, restoring it afterwards.
func withShortSpin(t *testing.T, fn func()) {
	t.Helper()
	saved := maxSpin
	maxSpin = 4
	defer func() { maxSpin = saved }()
	fn()
}

func TestOpenHappy(t *testing.T) {
	f := newFakeCRB()
	c, err := Open(f)
	if err != nil {
		t.Fatalf("Open err = %v", err)
	}
	if c.r != f {
		t.Fatalf("Open did not bind regs")
	}
}

func TestOpenNotCRB(t *testing.T) {
	f := newFakeCRB()
	f.ifaceType = 0 // FIFO/TIS
	if _, err := Open(f); err != ErrNotCRB {
		t.Fatalf("Open err = %v, want ErrNotCRB", err)
	}
}

func TestOpenNeverValid(t *testing.T) {
	f := newFakeCRB()
	f.neverValid = true
	withShortSpin(t, func() {
		if _, err := Open(f); err != ErrTimeout {
			t.Fatalf("Open err = %v, want ErrTimeout", err)
		}
	})
}

func TestSendHappy(t *testing.T) {
	f := newFakeCRB()
	c, err := Open(f)
	if err != nil {
		t.Fatalf("Open err = %v", err)
	}
	cmd := common.BuildCommand(
		uint16(common.TagNoSessions), uint32(common.CCGetRandom),
		[]byte{0x00, 0x02},
	)
	rsp, err := c.Send(cmd)
	if err != nil {
		t.Fatalf("Send err = %v", err)
	}
	if !bytes.Equal(rsp, f.response) {
		t.Fatalf("Send rsp = %x, want %x", rsp, f.response)
	}
	// The command must have been written into the data buffer before the
	// response overwrote it (they share one buffer).
	if !bytes.Equal(f.lastCmd[:len(cmd)], cmd) {
		t.Fatalf("command not written to data buffer")
	}
	// The interface must have been released (goIdle) and locality
	// relinquished.
	if f.reg[regLocState]&locStateLocAssigned != 0 {
		t.Fatalf("locality not relinquished")
	}
}

func TestSendNoLocality(t *testing.T) {
	f := newFakeCRB()
	c, _ := Open(f)
	f.neverGranted = true
	withShortSpin(t, func() {
		if _, err := c.Send(nil); err != ErrLocality {
			t.Fatalf("Send err = %v, want ErrLocality", err)
		}
	})
}

func TestSendNeverReady(t *testing.T) {
	f := newFakeCRB()
	c, _ := Open(f)
	f.neverReady = true
	withShortSpin(t, func() {
		if _, err := c.Send(nil); err != ErrNotReady {
			t.Fatalf("Send err = %v, want ErrNotReady", err)
		}
	})
}

func TestSendReadyError(t *testing.T) {
	f := newFakeCRB()
	c, _ := Open(f)
	f.readyError = true
	if _, err := c.Send(nil); err != ErrTPM {
		t.Fatalf("Send err = %v, want ErrTPM", err)
	}
}

func TestSendStartTimeout(t *testing.T) {
	f := newFakeCRB()
	c, _ := Open(f)
	f.neverDone = true
	cmd := common.BuildCommand(
		uint16(common.TagNoSessions), uint32(common.CCGetRandom), nil)
	withShortSpin(t, func() {
		if _, err := c.Send(cmd); err != ErrTimeout {
			t.Fatalf("Send err = %v, want ErrTimeout", err)
		}
	})
	// The timeout path must drive the CTRL_CANCEL handshake: set then
	// clear. TCG PTP, "CRB_CTRL_CANCEL_x".
	if len(f.cancelWrites) != 2 ||
		f.cancelWrites[0] != ctrlCancelSet ||
		f.cancelWrites[1] != ctrlCancelClear {
		t.Fatalf("cancel writes = %v", f.cancelWrites)
	}
}

func TestSendStsErrorAfterDone(t *testing.T) {
	f := newFakeCRB()
	c, _ := Open(f)
	f.stsErrorDone = true
	if _, err := c.Send(nil); err != ErrTPM {
		t.Fatalf("Send err = %v, want ErrTPM", err)
	}
}

func TestSendShortResponse(t *testing.T) {
	f := newFakeCRB()
	c, _ := Open(f)
	// A response header declaring a size below the 10-byte header.
	f.response = make([]byte, common.HeaderSize)
	// size field (u32 at offset 2) = 4 (< HeaderSize).
	copy(f.response[2:6], []byte{0x00, 0x00, 0x00, 0x04})
	if _, err := c.Send(nil); err != ErrShortResponse {
		t.Fatalf("Send err = %v, want ErrShortResponse", err)
	}
}

func TestSendResponseTooLarge(t *testing.T) {
	f := newFakeCRB()
	c, _ := Open(f)
	// A response header declaring a size larger than the data buffer.
	f.response = make([]byte, common.HeaderSize)
	big := uint32(bufSize + 1)
	f.response[2] = byte(big >> 24)
	f.response[3] = byte(big >> 16)
	f.response[4] = byte(big >> 8)
	f.response[5] = byte(big)
	if _, err := c.Send(nil); err != ErrResponseTooLarge {
		t.Fatalf("Send err = %v, want ErrResponseTooLarge", err)
	}
}

// TestSendImplementsTransport pins the interface satisfaction at runtime
// in addition to the compile-time assertion in crb.go.
func TestSendImplementsTransport(t *testing.T) {
	f := newFakeCRB()
	c, _ := Open(f)
	var _ common.Transport = c
}
