package logging

import (
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

type sqlQuery struct {
	query string
}

func (q sqlQuery) LogValue() slog.Value {
	query := q.query
	query = strings.TrimSpace(query)
	query = strings.ReplaceAll(query, "\n", " ")
	query = strings.ReplaceAll(query, "\t", " ")
	return slog.StringValue(query)
}

func (q sqlQuery) DebugLogValue() slog.Value {
	capitalizeAndNewlineWord := func(s, word string, addAfter string) string {
		re := regexp.MustCompile(`(?i)\s+` + word + `\s+`)
		match := re.FindStringIndex(s)
		if match != nil {
			before := strings.TrimSpace(
				s[:match[0]],
			) + " \n" // <left...><space><newline>
			mid := strings.ToUpper(strings.TrimSpace(s[match[0]:match[1]])) // <match>
			after := " " + addAfter + strings.TrimSpace(
				s[match[1]:],
			) // <space><addAfter><...right>

			return before + mid + after
		}
		return s
	}

	highlightPositionalParams := func(s string) string {
		re := regexp.MustCompile(`\?\d{1,3}`)
		return re.ReplaceAllString(s, ansiSqlBoundParam+"$0"+ansiReset)
	}

	highlightNamedParams := func(s string) string {
		re := regexp.MustCompile(`[$:@][0-9a-zA-Z_]+`)
		return re.ReplaceAllString(s, ansiSqlBoundParam+"$0"+ansiReset)
	}

	query := q.query
	query = strings.TrimSpace(query)
	query = capitalizeAndNewlineWord(query, "FROM", "")
	query = capitalizeAndNewlineWord(query, "WHERE", "\n   ")
	query = capitalizeAndNewlineWord(query, `ORDER\s+BY`, "")
	query = capitalizeAndNewlineWord(query, "LIMIT", "")
	query = capitalizeAndNewlineWord(query, `GROUP\s+BY`, "")
	query = capitalizeAndNewlineWord(query, "HAVING", "\n   ")

	query = highlightPositionalParams(query)
	query = highlightNamedParams(query)

	return slog.StringValue(query)
}

func SqlQuery(query string, args []any) slog.Attr {
	attrs := []slog.Attr{
		slog.Any("_sql", sqlQuery{query: query}),
	}
	for i, arg := range args {
		switch arg := arg.(type) {
		case sql.NamedArg:
			attrs = append(attrs, slog.Any(arg.Name, arg.Value))
		default:
			attrs = append(attrs, slog.Any(fmt.Sprintf("arg_%d", i), arg))
		}
	}
	return slog.GroupAttrs("query", attrs...)
}

func SqlQueryArgs(args []any) slog.Attr {
	attrs := []slog.Attr{}
	for i, arg := range args {
		switch arg := arg.(type) {
		case sql.NamedArg:
			attrs = append(attrs, slog.Any(arg.Name, arg.Value))
		default:
			attrs = append(attrs, slog.Any(fmt.Sprintf("arg_%d", i), arg))
		}
	}
	return slog.GroupAttrs("query", attrs...)
}
