package journal

import (
	"strings"
	"time"
)

// Field is one key and value of a line, past its time, level and message.
type Field struct{ Key, Value string }

// Entry is one line of the log, read back.
type Entry struct {
	Time    time.Time
	Level   string
	Message string
	Fields  []Field
	// OK is set when the line was logfmt, as the log writes it.
	OK bool
}

// Field is the value of a field by its key, or empty.
func (e Entry) Field(key string) string {
	for _, f := range e.Fields {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

// Parse reads one line of the log back: logfmt, keys bare and values bare or
// quoted, with time, level and msg lifted out of the fields.
func Parse(line string) Entry {
	var e Entry
	rest := strings.TrimSpace(line)
	for rest != "" {
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 || strings.ContainsAny(rest[:eq], " \"") {
			return Entry{}
		}
		key := rest[:eq]
		rest = rest[eq+1:]
		var val string
		if strings.HasPrefix(rest, `"`) {
			var b strings.Builder
			i := 1
			for ; i < len(rest) && rest[i] != '"'; i++ {
				if rest[i] == '\\' && i+1 < len(rest) {
					i++
				}
				b.WriteByte(rest[i])
			}
			if i >= len(rest) {
				return Entry{}
			}
			val, rest = b.String(), rest[i+1:]
		} else if sp := strings.IndexByte(rest, ' '); sp >= 0 {
			val, rest = rest[:sp], rest[sp:]
		} else {
			val, rest = rest, ""
		}
		rest = strings.TrimLeft(rest, " ")
		switch key {
		case "time":
			e.Time, _ = time.Parse(time.RFC3339Nano, val)
		case "level":
			e.Level = val
		case "msg":
			e.Message = val
		default:
			e.Fields = append(e.Fields, Field{key, val})
		}
	}
	e.OK = e.Level != ""
	return e
}
