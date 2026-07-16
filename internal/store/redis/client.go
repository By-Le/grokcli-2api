package redis

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const adminSessionTTLSeconds = 7 * 24 * 3600

type Client struct {
	URL    string
	Prefix string
}

func New(urlValue, prefix string) *Client {
	if strings.TrimSpace(prefix) == "" {
		prefix = "g2a"
	}
	return &Client{URL: strings.TrimSpace(urlValue), Prefix: strings.Trim(prefix, ":")}
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.command(ctx, "PING")
	return err
}

func (c *Client) VerifyAdminSession(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || c == nil || strings.TrimSpace(c.URL) == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	key := c.key("admin", "sess", token)
	value, err := c.command(ctx, "GET", key)
	if err != nil || value == "" {
		return false
	}
	_, _ = c.command(ctx, "EXPIRE", key, strconv.Itoa(adminSessionTTLSeconds))
	return true
}

func (c *Client) key(parts ...string) string {
	segments := []string{strings.Trim(c.Prefix, ":")}
	for _, part := range parts {
		part = strings.Trim(strings.TrimSpace(part), ":")
		if part != "" {
			segments = append(segments, part)
		}
	}
	return strings.Join(segments, ":")
}

func (c *Client) command(ctx context.Context, name string, args ...string) (string, error) {
	addr, password, db, err := parseRedisURL(c.URL)
	if err != nil {
		return "", err
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(conn)
	if password != "" {
		if err := writeRESP(conn, "AUTH", password); err != nil {
			return "", err
		}
		if _, err := readRESP(reader); err != nil {
			return "", err
		}
	}
	if db != "" && db != "0" {
		if err := writeRESP(conn, "SELECT", db); err != nil {
			return "", err
		}
		if _, err := readRESP(reader); err != nil {
			return "", err
		}
	}
	if err := writeRESP(conn, append([]string{name}, args...)...); err != nil {
		return "", err
	}
	return readRESP(reader)
}

func parseRedisURL(raw string) (addr, password, db string, err error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", "", err
	}
	if parsed.Scheme != "redis" && parsed.Scheme != "rediss" {
		return "", "", "", fmt.Errorf("unsupported Redis URL scheme %q", parsed.Scheme)
	}
	if parsed.Scheme == "rediss" {
		return "", "", "", errors.New("rediss is not supported by the built-in lightweight readiness client")
	}
	addr = parsed.Host
	if !strings.Contains(addr, ":") {
		addr += ":6379"
	}
	if parsed.User != nil {
		password, _ = parsed.User.Password()
		if password == "" {
			password = parsed.User.Username()
		}
	}
	db = strings.Trim(parsed.Path, "/")
	if db == "" {
		db = "0"
	}
	return addr, password, db, nil
}

func writeRESP(conn net.Conn, args ...string) error {
	var b strings.Builder
	b.WriteString("*")
	b.WriteString(strconv.Itoa(len(args)))
	b.WriteString("\r\n")
	for _, arg := range args {
		b.WriteString("$")
		b.WriteString(strconv.Itoa(len(arg)))
		b.WriteString("\r\n")
		b.WriteString(arg)
		b.WriteString("\r\n")
	}
	_, err := conn.Write([]byte(b.String()))
	return err
}

func readRESP(reader *bufio.Reader) (string, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return "", err
	}
	switch prefix {
	case '+':
		line, err := reader.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	case '-':
		line, _ := reader.ReadString('\n')
		return "", errors.New(strings.TrimRight(line, "\r\n"))
	case ':':
		line, err := reader.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	case '$':
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		length, err := strconv.Atoi(strings.TrimRight(line, "\r\n"))
		if err != nil {
			return "", err
		}
		if length < 0 {
			return "", nil
		}
		buf := make([]byte, length+2)
		if _, err := reader.Read(buf); err != nil {
			return "", err
		}
		return string(buf[:length]), nil
	default:
		return "", fmt.Errorf("unexpected RESP prefix %q", prefix)
	}
}
