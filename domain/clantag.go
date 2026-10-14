package domain

import (
	"regexp"
	"strings"
)

// clanTagPatterns cover the handful of nickname conventions BF1942/BFV clans
// actually use: a short alphanumeric tag wrapped in a delimiter pair, glued
// to either end of the name (e.g. "[TAG]Player", "Player-TAG-",
// "=TAG=Player", "{TAG}Player"). Bracket forms are tried first since they're
// by far the most common in these logs.
var clanTagPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\[([A-Za-z0-9_]{2,6})\]`),
	regexp.MustCompile(`\[([A-Za-z0-9_]{2,6})\]$`),
	regexp.MustCompile(`^-([A-Za-z0-9_]{2,6})-`),
	regexp.MustCompile(`-([A-Za-z0-9_]{2,6})-$`),
	regexp.MustCompile(`^=([A-Za-z0-9_]{2,6})=`),
	regexp.MustCompile(`=([A-Za-z0-9_]{2,6})=$`),
	regexp.MustCompile(`^\{([A-Za-z0-9_]{2,6})\}`),
	regexp.MustCompile(`\{([A-Za-z0-9_]{2,6})\}$`),
}

// ExtractClanTag pulls a clan tag out of a raw in-game nickname, or returns
// "" if the name doesn't match any recognized convention. Tags are
// normalized to uppercase so "[Tag]Foo" and "[TAG]Bar" group together.
func ExtractClanTag(displayName string) string {
	for _, re := range clanTagPatterns {
		if m := re.FindStringSubmatch(displayName); m != nil {
			return strings.ToUpper(m[1])
		}
	}
	return ""
}
