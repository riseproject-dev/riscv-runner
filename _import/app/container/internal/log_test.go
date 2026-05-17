package internal

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestInitSlog_Levels(t *testing.T) {
	cases := []struct {
		level   string
		emitDbg bool
		emitWrn bool
	}{
		{"DEBUG", true, true},
		{"debug", true, true},
		{"INFO", false, true},
		{"", false, true},
		{"warn", false, true},
		{"WARNING", false, true},
		{"ERROR", false, false},
		{"garbage", false, true}, // default → INFO
	}
	for _, c := range cases {
		t.Run(c.level, func(t *testing.T) {
			var buf bytes.Buffer
			InitSlog(c.level, &buf)
			slog.Debug("dbg-line")
			slog.Warn("wrn-line")
			slog.Error("err-line")
			s := buf.String()
			if strings.Contains(s, "dbg-line") != c.emitDbg {
				t.Errorf("debug emit: got=%v want=%v out=%q", !c.emitDbg, c.emitDbg, s)
			}
			if strings.Contains(s, "wrn-line") != c.emitWrn {
				t.Errorf("warn emit: got=%v want=%v out=%q", !c.emitWrn, c.emitWrn, s)
			}
			if !strings.Contains(s, "err-line") {
				t.Errorf("error always emits: %q", s)
			}
		})
	}
}
