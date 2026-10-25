package shnum

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// bashEnv is the environment of the differential cases: set names, a
// chain, an empty one, a self reference and values with blanks and errors.
var bashEnv = map[string]string{"FOO": "5", "A": "B", "B": "7", "SELF": "SELF", "SP": " 5 ", "BLANK": "  ", "OCT": "010", "EMP": "",
	"WORD": "abc", "EXPR": "1+2", "BAD": "08", "BADSYN": "1 +", "NEG": "-3", "NEGSP": "- 3", "NL": "\n7\n"}

func lookupIn(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

// Bash runs one statement under /bin/bash (tests only) with env and returns
// its stdout and whether it succeeded. The test is skipped without bash.
func Bash(t *testing.T, env map[string]string, script string) (string, bool) {
	t.Helper()
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("no /bin/bash for the differential cases")
	}
	cmd := exec.Command("/bin/bash", "-c", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n"), err == nil
}

// arithCases are the expressions every differential test runs.
var arithCases = []string{
	"30", " 30 ", "\t30\n", "007", "010", "08", "0x1f", "0X10", "0x", "0xg", "2#0", "2#101", "2#2", "64#@_", "64#Z", "36#Z", "36#z", "37#z",
	"10#08", "1#1", "65#1", "0#1", "10#", "1e3", "3a",
	"9223372036854775807", "-9223372036854775808", "9223372036854775808", "9223372036854775807+1", "18446744073709551617",
	"- 1", "-1", "+1", "--1", "- -1", "+-1", "-+1", "!-1", "!0", "~0", "~5", "1-1", "3-2-1", "2*3%4", "-5/2", "5%-3", "-9223372036854775808/-1",
	"-9223372036854775808%-1", "1/0", "1%0", "2**3", "2**3**2", "-2**2", "2**-1", "2**63", "2**64", "0**0", "3**40",
	"1<<3", "1<<64", "1<<65", "-1>>1", "-8>>1", "1<<-1", "7>3>1", "1<2", "2<=2", "3>=4", "1==1", "1!=1", "6&3", "6|3", "6^3", "1&&2", "0&&1",
	"1||0", "0||0", "1?2:3", "0?2:3", "1?2:1?3:4", "0?1:0?3:4", "(1,2)", "1,2,3", "(1+2)*3", "((4))", "", "   ", "1 2", "1+", "+", "(1", "1)",
	"1?2", "1?:3", "?", "1+(2", "$", "1=2", "(x)=1", "1+=2",
	"FOO", "FOO*2", "A", "SELF", "SP", "SP*2", "BLANK", "BLANK+1", "OCT", "EMP", "EMP+1", "WORD", "UNSET", "UNSET+1", "EXPR*2", "BAD", "BADSYN",
	"NEG", "NEGSP", "NL", "-FOO", "!FOO", "FOO[0]", "FOO[1]", "FOO[0+0]", "FOO[1/0]", "x=5", "x=5, x*2", "x+=2, x", "FOO+=2", "FOO++", "FOO++, FOO",
	"++FOO", "--FOO, FOO", "++UNSET", "x = 1 , x", "0 && (x=5), x", "1 || (x=5), x", "0?(x=1):2, x", "0&&1/0", "1||1/0", "0?1/0:2", "1?2:1/0",
	"0 && BAD", "1 || BAD", "0 ? BAD : 1", "x=FOO, x", "y=x=3, y", "FOO*=3", "FOO/=0", "FOO%=2", "FOO<<=2", "FOO>>=1", "FOO&=4", "FOO^=1", "FOO|=2",
	"FOO-=1", "x=(1,2)", "a b", "FOO FOO", "1?2:", "-- FOO", "++ FOO, FOO", "FOO --",
}

func TestEvalMatchesBash(t *testing.T) {
	if out, ok := Bash(t, bashEnv, "echo $(( 2**3**2 + FOO ))"); !ok || out != "517" {
		t.Fatalf("the bash control: %q %v", out, ok)
	}
	for _, expr := range arithCases {
		want, ok := Bash(t, bashEnv, "echo $(( "+expr+" ))")
		got, err := Eval(expr, lookupIn(bashEnv))
		if ok != (err == nil) || (ok && want != strconv.FormatInt(got, 10)) {
			t.Errorf("$(( %q )): bash %q (ok %v), Go %d (%v)", expr, want, ok, got, err)
		}
	}
}

func TestEvalErrors(t *testing.T) {
	if _, err := Eval("1/0", nil); err == nil || err.Error() != "1/0: division by 0 (error token is \"0\")" {
		t.Errorf("%v", err)
	}
	// The recursion limit counts the expansions of names.
	env := map[string]string{"N0": "1"}
	for i := 1; i <= 1030; i++ {
		env["N"+strconv.Itoa(i)] = "N" + strconv.Itoa(i-1)
	}
	if v, err := Eval("N1000", lookupIn(env)); err != nil || v != 1 {
		t.Errorf("a chain of 1000 names: %d %v", v, err)
	}
	if _, err := Eval("N1030", lookupIn(env)); err == nil || !strings.Contains(err.Error(), "recursion level exceeded") {
		t.Errorf("a chain past the limit: %v", err)
	}
	if v, err := Eval("FOO+1", nil); err != nil || v != 1 {
		t.Errorf("no lookup: %d %v", v, err)
	}
	if _, err := Eval("FOO[1", lookupIn(bashEnv)); err == nil {
		t.Error("a subscript without its ]")
	}
}

// TestEvalErrorTextMatchesBash: an error reads as bash's line, after its
// "<script>: line N: " prefix: the expression, the message and the token.
func TestEvalErrorTextMatchesBash(t *testing.T) {
	for _, expr := range []string{"1/0", " 1/0 ", "1/0 ", "08", " 08 + 1", "1 + 09 * 2", "1 + 0#1", "3 + 65#1 ", "x+ ", "2**-1 ",
		"1 2 ", "1 ? 2 ", "a = 1 +", "1%0", "BAD"} {
		Bash(t, nil, "true") // skips without bash
		cmd := exec.Command("/bin/bash", "-c", "echo $(("+expr+"))")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "BAD=1/0"}
		b, _ := cmd.CombinedOutput()
		want := strings.TrimPrefix(strings.TrimRight(string(b), "\n"), "/bin/bash: ")
		_, err := Eval(expr, lookupIn(map[string]string{"BAD": "1/0"}))
		if err == nil || err.Error() != want {
			t.Errorf("$((%s)): bash %q, Go %v", expr, want, err)
		}
	}
}
