// Package gamespy speaks the BF1942 GameSpy v1 UDP query protocol: a
// "\status\" probe answered with backslash-delimited key/value pairs,
// possibly split across packets ordered by \queryid\N.M and terminated by
// \final\. Shared by apps/api's /live page and apps/worker's server
// up/down watcher.
package gamespy

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

func Query(host string, port int, timeout time.Duration) (map[string]string, error) {
	conn, err := net.DialTimeout("udp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write([]byte(`\status\`)); err != nil {
		return nil, err
	}

	fragments := map[int]string{}
	sawFinal := false
	buf := make([]byte, 64<<10)
	for !sawFinal {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		packet := string(buf[:n])
		if strings.Contains(packet, `\final\`) {
			sawFinal = true
		}
		seq := len(fragments) + 1
		if i := strings.LastIndex(packet, `\queryid\`); i >= 0 {
			id := packet[i+len(`\queryid\`):]
			id = strings.Trim(id, `\`)
			if dot := strings.LastIndex(id, "."); dot >= 0 {
				if parsed, err := strconv.Atoi(id[dot+1:]); err == nil {
					seq = parsed
				}
			}
			packet = packet[:i]
		}
		packet = strings.ReplaceAll(packet, `\final\`, ``)
		fragments[seq] = packet
	}

	seqs := make([]int, 0, len(fragments))
	for seq := range fragments {
		seqs = append(seqs, seq)
	}
	sort.Ints(seqs)
	var full strings.Builder
	for _, seq := range seqs {
		full.WriteString(fragments[seq])
	}

	values := map[string]string{}
	tokens := strings.Split(full.String(), `\`)
	for i := 1; i+1 < len(tokens); i += 2 {
		key := strings.ToLower(strings.TrimSpace(tokens[i]))
		if key == "" || key == "queryid" || key == "final" {
			continue
		}
		if _, exists := values[key]; !exists {
			values[key] = tokens[i+1]
		}
	}
	return values, nil
}
