package i18n

import (
	"fmt"
	"strings"
)

// format fills {placeholders} from args.
//
// A template whose placeholders are not all supplied comes back untouched
// rather than half-filled. A sentence with a literal "{scene}" left in it is
// odd to hear, but it is audible and it points at the bug, which a blank or
// truncated string would not.
func format(template string, args Args) string {
	if !strings.ContainsRune(template, '{') {
		return template
	}

	var builder strings.Builder
	builder.Grow(len(template))

	for index := 0; index < len(template); {
		open := strings.IndexByte(template[index:], '{')
		if open < 0 {
			builder.WriteString(template[index:])
			break
		}
		open += index

		end := strings.IndexByte(template[open:], '}')
		if end < 0 {
			builder.WriteString(template[index:])
			break
		}
		end += open

		value, ok := args[template[open+1:end]]
		if !ok {
			return template
		}
		builder.WriteString(template[index:open])
		builder.WriteString(stringify(value))
		index = end + 1
	}
	return builder.String()
}

func stringify(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprintf("%v", typed)
	}
}
