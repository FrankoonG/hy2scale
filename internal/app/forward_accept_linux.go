package app

import (
	"encoding/binary"
	"fmt"
	"log"
	"unsafe"

	"golang.org/x/sys/unix"
)

// setForwardAccept uses raw setsockopt to set the iptables filter table
// FORWARD chain policy to ACCEPT. This is needed on iKuai containers where
// iptables binaries cannot modify the filter table (custom kernel targets),
// but the kernel still enforces the FORWARD chain DROP policy set by Docker.
//
// Docker 18.09.9 sets FORWARD policy to DROP when creating containers.
// This function replaces the entire filter table with a minimal version
// that has all three chains (INPUT, FORWARD, OUTPUT) with ACCEPT policy.
func setForwardAccept() error {
	// Open raw socket for iptables control
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_RAW, unix.IPPROTO_RAW)
	if err != nil {
		return fmt.Errorf("socket: %w", err)
	}
	defer unix.Close(fd)

	// First, get table info to learn the current structure
	const IPT_SO_GET_INFO = 64  // IPT_BASE_CTL
	const IPT_SO_SET_REPLACE = 64 // IPT_BASE_CTL

	// ipt_getinfo structure (name[32] + fields)
	var info [84]byte
	copy(info[:], "filter")
	infoLen := uint32(len(info))

	// Try to get info (may fail on iKuai but we try anyway)
	err = getsockoptRaw(fd, unix.IPPROTO_IP, IPT_SO_GET_INFO, info[:], &infoLen)
	if err != nil {
		// Can't read table info, construct minimal table from scratch
		log.Printf("[forward-accept] getsockopt GET_INFO failed: %v, building minimal table", err)
	}

	// Build a minimal filter table with 3 chains, all ACCEPT, no rules
	// Structure: ipt_replace header + 3 entries (one per chain: INPUT, FORWARD, OUTPUT)
	//
	// Each entry is: ipt_entry (112 bytes on x86_64) + ipt_standard_target (40 bytes)
	// Total per entry = 152 bytes
	// 3 entries = 456 bytes
	//
	// ipt_replace layout:
	//   char name[32]
	//   uint32 valid_hooks  = 0x0E (INPUT=1 | FORWARD=2 | OUTPUT=4)
	//   uint32 num_entries  = 3
	//   uint32 size         = 3 * entry_size
	//   uint32 hook_entry[5]  (offsets to each chain's first entry)
	//   uint32 underflow[5]   (offsets to each chain's last/policy entry)
	//   uint32 num_counters = 3
	//   ipt_counters[] = 3 * {0,0}
	//   entries...

	const entrySize = 152 // ipt_entry(112) + ipt_standard_target(40) on x86_64
	const numEntries = 3
	const entriesSize = numEntries * entrySize

	// Total replace size: 32 + 4 + 4 + 4 + 5*4 + 5*4 + 4 + 3*16 + entries
	// = 32 + 4 + 4 + 4 + 20 + 20 + 4 + 48 + 456 = 592
	const headerSize = 32 + 4 + 4 + 4 + 20 + 20 + 4  // = 88
	const counterSize = numEntries * 16 // 48
	replaceSize := headerSize + counterSize + entriesSize
	replace := make([]byte, replaceSize)

	off := 0
	// name[32] = "filter"
	copy(replace[off:], "filter")
	off = 32
	// valid_hooks = INPUT|FORWARD|OUTPUT = 0x0E
	binary.LittleEndian.PutUint32(replace[off:], 0x0E)
	off += 4
	// num_entries = 3
	binary.LittleEndian.PutUint32(replace[off:], numEntries)
	off += 4
	// size = entries total size
	binary.LittleEndian.PutUint32(replace[off:], uint32(entriesSize))
	off += 4
	// hook_entry[5]: INPUT=0, FORWARD=entrySize, OUTPUT=2*entrySize, others=0
	binary.LittleEndian.PutUint32(replace[off:], 0)                        // INPUT
	binary.LittleEndian.PutUint32(replace[off+4:], uint32(entrySize))      // FORWARD
	binary.LittleEndian.PutUint32(replace[off+8:], uint32(2*entrySize))    // OUTPUT
	binary.LittleEndian.PutUint32(replace[off+12:], 0)                     // unused
	binary.LittleEndian.PutUint32(replace[off+16:], 0)                     // unused
	off += 20
	// underflow[5]: same as hook_entry (one entry per chain = policy entry)
	binary.LittleEndian.PutUint32(replace[off:], 0)                        // INPUT
	binary.LittleEndian.PutUint32(replace[off+4:], uint32(entrySize))      // FORWARD
	binary.LittleEndian.PutUint32(replace[off+8:], uint32(2*entrySize))    // OUTPUT
	binary.LittleEndian.PutUint32(replace[off+12:], 0)
	binary.LittleEndian.PutUint32(replace[off+16:], 0)
	off += 20
	// num_counters = 3
	binary.LittleEndian.PutUint32(replace[off:], numEntries)
	off += 4
	// counters[3] = all zeros (16 bytes each: uint64 pcnt + uint64 bcnt)
	off += counterSize

	// Now build 3 entries (INPUT, FORWARD, OUTPUT), all with ACCEPT verdict (-1 = ACCEPT)
	for i := 0; i < numEntries; i++ {
		entryOff := off + i*entrySize
		// ipt_entry: mostly zeros (match-all)
		// target_offset = 112 (sizeof ipt_entry on x86_64)
		// next_offset = 152 (target_offset + sizeof ipt_standard_target)
		binary.LittleEndian.PutUint16(replace[entryOff+104:], 112) // target_offset
		binary.LittleEndian.PutUint16(replace[entryOff+106:], uint16(entrySize)) // next_offset

		// ipt_standard_target at offset 112 within the entry
		tgtOff := entryOff + 112
		// xt_entry_target: target_size=40
		binary.LittleEndian.PutUint16(replace[tgtOff:], 40) // u.target_size
		// name = "" (standard target)
		// verdict = -1 - 1 = -2 (NF_ACCEPT) ... actually:
		// Standard verdict: -NF_ACCEPT-1 = -1-1 = -2? No:
		// NF_DROP=0, NF_ACCEPT=1
		// verdict = -(NF_ACCEPT+1) = -2 for ACCEPT
		// In ipt_standard_target, verdict is int32 at offset 32 within target
		verdict := int32(-2) // -NF_ACCEPT-1 = -(1)-1 = -2
		binary.LittleEndian.PutUint32(replace[tgtOff+32:], uint32(verdict))
	}

	// Call setsockopt
	_, _, errno := unix.Syscall6(
		unix.SYS_SETSOCKOPT,
		uintptr(fd),
		uintptr(unix.IPPROTO_IP),
		uintptr(IPT_SO_SET_REPLACE),
		uintptr(unsafe.Pointer(&replace[0])),
		uintptr(len(replace)),
		0,
	)
	if errno != 0 {
		return fmt.Errorf("setsockopt SET_REPLACE: %v", errno)
	}

	log.Printf("[forward-accept] successfully set FORWARD chain policy to ACCEPT")
	return nil
}

func getsockoptRaw(fd int, level, name int, val []byte, vallen *uint32) error {
	_, _, errno := unix.Syscall6(
		unix.SYS_GETSOCKOPT,
		uintptr(fd),
		uintptr(level),
		uintptr(name),
		uintptr(unsafe.Pointer(&val[0])),
		uintptr(unsafe.Pointer(vallen)),
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
