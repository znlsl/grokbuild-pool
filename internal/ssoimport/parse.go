// Package ssoimport 解析 SSO cookie/token 输入，经 HTTP 转换器转为账号。
package ssoimport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrEntryLimit 在 SSO 值数量超过 limit 时返回。
var ErrEntryLimit = errors.New("ssoimport: entry limit exceeded")

// ErrValueLimit 在单个 SSO 值超过配置上限时返回。
var ErrValueLimit = errors.New("ssoimport: SSO value limit exceeded")

// ErrConverterRequired 在 SSO 转换需要远程转换器但未配置时返回。
var ErrConverterRequired = errors.New("ssoimport: SSO converter is not configured (set --converter-url); offline conversion is not supported")

// ParseSSOValues 从换行文本或 JSON 提取 SSO cookie/token 字符串。
// limit <= 0 表示无界。
func ParseSSOValues(data []byte, limit int) ([]string, error) {
	return ParseSSOValuesBounded(data, limit, 0)
}

// ParseSSOValuesBounded 同时限制条目数和单个 SSO 值字节数。
func ParseSSOValuesBounded(data []byte, limit, maxValueBytes int) ([]string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("ssoimport: SSO document is empty")
	}
	var values []string
	jsonLike := trimmed[0] == '[' || trimmed[0] == '{'
	if jsonLike {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		if err := collectSSOJSONValue(decoder, &values, limit, maxValueBytes); err != nil {
			if errors.Is(err, ErrEntryLimit) || errors.Is(err, ErrValueLimit) {
				return nil, err
			}
			return nil, fmt.Errorf("ssoimport: invalid JSON SSO document")
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("ssoimport: invalid JSON SSO document")
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("ssoimport: no explicit SSO values found in JSON document")
		}
	}
	if !jsonLike {
		scanner := bufio.NewScanner(bytes.NewReader(trimmed))
		maxLineBytes := len(trimmed)
		if maxLineBytes < int(^uint(0)>>1) {
			maxLineBytes++
		}
		bufCap := 64 * 1024
		if maxLineBytes < bufCap {
			bufCap = maxLineBytes
		}
		scanner.Buffer(make([]byte, bufCap), maxLineBytes)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if limit > 0 && len(values) >= limit {
				return nil, ErrEntryLimit
			}
			if maxValueBytes > 0 && len(line) > maxValueBytes {
				return nil, fmt.Errorf("%w (max %d bytes)", ErrValueLimit, maxValueBytes)
			}
			value := normalizeSSOValue(line)
			if maxValueBytes > 0 && len(value) > maxValueBytes {
				return nil, fmt.Errorf("%w (max %d bytes)", ErrValueLimit, maxValueBytes)
			}
			values = append(values, value)
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("ssoimport: SSO value exceeds %d bytes or document cannot be read", maxValueBytes)
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("ssoimport: no SSO values found")
	}
	return values, nil
}

func collectSSOJSONValue(decoder *json.Decoder, out *[]string, limit, maxValueBytes int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch typed := token.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		if limit > 0 && len(*out) >= limit {
			return ErrEntryLimit
		}
		if maxValueBytes > 0 && len(typed) > maxValueBytes {
			return fmt.Errorf("%w (max %d bytes)", ErrValueLimit, maxValueBytes)
		}
		value := normalizeSSOValue(typed)
		if maxValueBytes > 0 && len(value) > maxValueBytes {
			return fmt.Errorf("%w (max %d bytes)", ErrValueLimit, maxValueBytes)
		}
		*out = append(*out, value)
	case json.Delim:
		switch typed {
		case '[':
			for decoder.More() {
				if err := collectSSOJSONValue(decoder, out, limit, maxValueBytes); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '{':
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok {
					return fmt.Errorf("object member name is not a string")
				}
				if isSSOField(name) {
					if err := collectSSOJSONValue(decoder, out, limit, maxValueBytes); err != nil {
						return err
					}
				} else {
					var ignored json.RawMessage
					if err := decoder.Decode(&ignored); err != nil {
						return err
					}
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
	}
	return nil
}

func isSSOField(name string) bool {
	switch name {
	case "sso", "sso_token", "cookie", "token", "cookies":
		return true
	default:
		return false
	}
}

func normalizeSSOValue(value string) string {
	value = strings.TrimSpace(value)
	if strings.Count(value, "----") >= 2 {
		if index := strings.LastIndex(value, "----"); index >= 0 {
			if sso := strings.TrimSpace(value[index+4:]); sso != "" {
				return sso
			}
		}
	}
	return value
}
