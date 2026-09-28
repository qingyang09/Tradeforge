package types

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration 包装 time.Duration，使其在 JSON 中表现为人类可读的字符串（"24h"、"7d" 等价的 "168h"）。
//
// 标准库的 time.Duration 会被序列化成纳秒整数，写进策略配置里既不可读、
// 也容易让 LLM 生成出量级完全错误的值。
type Duration time.Duration

// D 是构造 Duration 的语法糖。
func D(d time.Duration) Duration { return Duration(d) }

// Std 返回底层的 time.Duration。
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String 实现 fmt.Stringer。
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON 把时长写成字符串。
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON 接受字符串形式（"90m"）或纳秒整数形式。
func (d *Duration) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case string:
		parsed, err := time.ParseDuration(x)
		if err != nil {
			return fmt.Errorf("无法解析时长 %q：%w", x, err)
		}
		*d = Duration(parsed)
		return nil
	case float64:
		*d = Duration(time.Duration(x))
		return nil
	case nil:
		*d = 0
		return nil
	default:
		return fmt.Errorf("时长字段期望字符串或数字，得到 %T", v)
	}
}
