package exedb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const inviteCodeMaxLen = 32

// randU32 returns a cryptographically secure random uint32.
func randU32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// randN returns a random int in [0, n) using rejection sampling.
func randN(n int) int {
	if n <= 0 {
		return 0
	}
	limit := uint32(^uint32(0)) - (uint32(^uint32(0)) % uint32(n))
	for {
		x := randU32()
		if x < limit {
			return int(x % uint32(n))
		}
	}
}

// randHexDigits returns a random hex string of the given number of digits.
func randHexDigits(digits int) string {
	if digits%2 != 0 || digits <= 0 {
		panic("digits must be positive and even")
	}
	bytes := digits / 2
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	out := make([]byte, hex.EncodedLen(len(buf)))
	hex.Encode(out, buf)
	return string(out)
}

func pick[T any](xs []T) T { return xs[randN(len(xs))] }

// Invite code templates that look like asm-ish program fragments.

var regs3 = []string{"rax", "rbx", "rcx", "rdx", "rsi", "rdi"}

var rots = []string{
	"05", "07", "0d", "11", "13", "15", "17", "1b",
}

// tmplXorRolCmp: xor r,r ; rol r,imm ; cmp r,0xXXXXXX
func tmplXorRolCmp() string {
	r := pick(regs3)
	rot := pick(rots)
	imm := randHexDigits(6)
	return fmt.Sprintf("xor-%s-%s-rol-%s-%s-0x%s", r, r, r, rot, imm)
}

// tmplCallPopAdd: call <lbl> ; pop r ; add r,0xXXXXXX
func tmplCallPopAdd() string {
	regs := []string{"rbx", "rdi", "rsi"}
	r := pick(regs)
	lbl := randHexDigits(2)
	imm := randHexDigits(6)
	return fmt.Sprintf("call-%s-pop-%s-add-%s-0x%s", lbl, r, r, imm)
}

// tmplSyscallTest: mov rax,0xSS ; syscall ; test rax,rax ; jne <lbl>
func tmplSyscallTest() string {
	syscalls := []string{
		"3b",  // execve
		"39",  // fork
		"3c",  // exit
		"00",  // read
		"01",  // write
		"101", // openat
	}
	var sc string
	if randN(2) == 0 {
		sc = pick(syscalls)
	} else {
		sc = randHexDigits(2)
	}
	lbl := randHexDigits(2)
	return fmt.Sprintf("mov-rax-0x%s-syscall-test-rax-rax-jne-%s", sc, lbl)
}

// tmplAddXor: add r1,r2 ; xor r2,r1 ; 0xXXXXXX
func tmplAddXor() string {
	pairs := [][2]string{
		{"rax", "rcx"},
		{"rax", "rdx"},
		{"rbx", "rsi"},
		{"rcx", "rdi"},
		{"rdx", "rsi"},
	}
	p := pick(pairs)
	imm := randHexDigits(6)
	return fmt.Sprintf("add-%s-%s-xor-%s-%s-0x%s", p[0], p[1], p[1], p[0], imm)
}

// tmplLeaRipJmp: lea r,[rip+imm] ; jmp lbl
func tmplLeaRipJmp() string {
	r := pick([]string{"rdi", "rsi", "rbx"})
	imm := randHexDigits(6)
	lbl := randHexDigits(2)
	return fmt.Sprintf("lea-%s-rip-0x%s-jmp-%s", r, imm, lbl)
}

type inviteCodeTemplate func() string

var inviteCodeTemplates = []inviteCodeTemplate{
	tmplXorRolCmp,
	tmplCallPopAdd,
	tmplSyscallTest,
	tmplAddXor,
	tmplLeaRipJmp,
}

// generateInviteCode generates a single random invite code.
func generateInviteCode() string {
	for {
		t := inviteCodeTemplates[randN(len(inviteCodeTemplates))]
		code := t()

		if len(code) > inviteCodeMaxLen {
			continue
		}

		// Only allow [a-z0-9-]
		ok := true
		for i := 0; i < len(code); i++ {
			c := code[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
				continue
			}
			ok = false
			break
		}
		if !ok {
			continue
		}

		return code
	}
}

// GenerateUniqueInviteCode generates a random invite code and ensures it doesn't
// already exist in the invite_codes table. It retries up to maxRetries times.
func (q *Queries) GenerateUniqueInviteCode(ctx context.Context) (string, error) {
	const maxRetries = 100
	for range maxRetries {
		code := generateInviteCode()

		// Check if code already exists
		_, err := q.GetInviteCodeByCode(ctx, code)
		if err != nil {
			// sql.ErrNoRows means code doesn't exist, which is what we want
			return code, nil
		}
		// Code exists, retry
	}
	return "", fmt.Errorf("failed to generate unique invite code after %d attempts", maxRetries)
}
