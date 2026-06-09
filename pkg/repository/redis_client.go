package repository

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

type RedisClient struct {
	addr     string
	password string
	db       int
	timeout  time.Duration
	tls      bool
}

type RedisClientConfig struct {
	Addr     string
	Password string
	DB       int
	Timeout  time.Duration
	TLS      bool
}

func NewRedisClient(cfg RedisClientConfig) *RedisClient {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:6379"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}
	return &RedisClient{
		addr:     cfg.Addr,
		password: cfg.Password,
		db:       cfg.DB,
		timeout:  cfg.Timeout,
		tls:      cfg.TLS,
	}
}

func (c *RedisClient) Do(ctx context.Context, args ...string) (interface{}, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	if c.password != "" {
		if err := c.writeCommand(conn, "AUTH", c.password); err != nil {
			return nil, err
		}
		if _, err := readRESP(reader); err != nil {
			return nil, err
		}
	}
	if c.db > 0 {
		if err := c.writeCommand(conn, "SELECT", strconv.Itoa(c.db)); err != nil {
			return nil, err
		}
		if _, err := readRESP(reader); err != nil {
			return nil, err
		}
	}
	if err := c.writeCommand(conn, args...); err != nil {
		return nil, err
	}
	return readRESP(reader)
}

func (c *RedisClient) DoBatch(ctx context.Context, commands ...[]string) ([]interface{}, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	if c.password != "" {
		if err := c.writeCommand(conn, "AUTH", c.password); err != nil {
			return nil, err
		}
		if _, err := readRESP(reader); err != nil {
			return nil, err
		}
	}
	if c.db > 0 {
		if err := c.writeCommand(conn, "SELECT", strconv.Itoa(c.db)); err != nil {
			return nil, err
		}
		if _, err := readRESP(reader); err != nil {
			return nil, err
		}
	}
	for _, command := range commands {
		if err := c.writeCommand(conn, command...); err != nil {
			return nil, err
		}
	}
	values := make([]interface{}, 0, len(commands))
	for range commands {
		value, err := readRESP(reader)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (c *RedisClient) dial(ctx context.Context) (net.Conn, error) {
	dialer := net.Dialer{Timeout: c.timeout}
	if deadline, ok := ctx.Deadline(); ok {
		dialer.Deadline = deadline
	}
	if c.tls {
		return tls.DialWithDialer(&dialer, "tcp", c.addr, &tls.Config{MinVersion: tls.VersionTLS12})
	}
	return dialer.DialContext(ctx, "tcp", c.addr)
}

func (c *RedisClient) writeCommand(w io.Writer, args ...string) error {
	if len(args) == 0 {
		return errors.New("redis command is empty")
	}
	if _, err := fmt.Fprintf(w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(w, "$%d\r\n%s\r\n", len(arg), arg); err != nil {
			return err
		}
	}
	return nil
}

func readRESP(r *bufio.Reader) (interface{}, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")

	switch prefix {
	case '+':
		return line, nil
	case '-':
		return nil, errors.New(line)
	case ':':
		return strconv.ParseInt(line, 10, 64)
	case '$':
		size, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if size < 0 {
			return nil, nil
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:size]), nil
	case '*':
		size, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if size < 0 {
			return nil, nil
		}
		values := make([]interface{}, 0, size)
		for i := 0; i < size; i++ {
			value, err := readRESP(r)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("unsupported redis response prefix %q", prefix)
	}
}
