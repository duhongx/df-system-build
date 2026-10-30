package componentprobe

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func redisValue(r *bufio.Reader) (any, error) {
	budget := 4 << 20
	return readRedisValue(r, 0, &budget)
}
func readRedisValue(r *bufio.Reader, depth int, budget *int) (any, error) {
	if depth > 8 || *budget <= 0 {
		return nil, fmt.Errorf("Redis 响应超过解析限制")
	}
	data, err := r.ReadSlice('\n')
	if err != nil || len(data) < 3 || len(data) > 4096 || !strings.HasSuffix(string(data), "\r\n") {
		return nil, fmt.Errorf("Redis 响应头无效")
	}
	*budget -= len(data)
	if *budget < 0 {
		return nil, fmt.Errorf("Redis 响应超过大小限制")
	}
	line := string(data[:len(data)-2])
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return nil, fmt.Errorf("Redis 命令被拒绝，请核对访问权限")
	case ':':
		v, err := strconv.ParseInt(line[1:], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("Redis 整数响应无效")
		}
		return v, nil
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil || n < -1 || n > *budget-2 {
			return nil, fmt.Errorf("Redis 响应长度异常")
		}
		if n == -1 {
			return nil, nil
		}
		b := make([]byte, n+2)
		if _, err = io.ReadFull(r, b); err != nil || string(b[n:]) != "\r\n" {
			return nil, fmt.Errorf("Redis 响应内容无效")
		}
		*budget -= n + 2
		return string(b[:n]), nil
	case '*':
		n, err := strconv.Atoi(line[1:])
		if err != nil || n < -1 || n > 10000 || n > *budget/3 {
			return nil, fmt.Errorf("Redis 数组响应超过限制")
		}
		if n == -1 {
			return nil, nil
		}
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			value, err := readRedisValue(r, depth+1, budget)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	}
	return nil, fmt.Errorf("Redis 响应类型不支持")
}
