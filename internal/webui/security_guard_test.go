package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNeverCallsCrossUserStorageQueries 把"internal/storage 里不按 user_id 过滤的
// 跨用户查询方法绝不能从 internal/webui 调用"这条规则从纯文档变成会挂红的强制检查——
// webui 的每个 handler 都必须锚定在当前登录用户的 user_id 上，调用这类方法等于让一个
// 用户看到/影响到另一个用户的数据。
//
// 目前有两个这样的方法：storage.Store.ListStrategiesByStateAllUsers（供
// cmd/executor/cmd/signal-engine 的多用户并发执行改造使用）和
// storage.Store.GetStrategyAllUsers（供 cmd/notifier 使用，见 internal/storage/
// postgres.go 上各自的 SECURITY 注释）——以后新增同类方法，把方法名加进下面这个
// 列表即可。
func TestNeverCallsCrossUserStorageQueries(t *testing.T) {
	forbidden := []string{
		"ListStrategiesByStateAllUsers",
		"GetStrategyAllUsers",
	}

	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if path == "security_guard_test.go" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range forbidden {
			if strings.Contains(string(content), name) {
				t.Errorf("%s 引用了跨用户查询方法 %q——internal/webui 的 handler 必须按当前登录用户的 "+
					"user_id 查询，绝不能跨用户读取数据", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 internal/webui 目录失败：%v", err)
	}
}
