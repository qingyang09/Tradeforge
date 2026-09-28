package backtest

import (
	"bufio"
	"encoding/json"
	"io"
)

// WriteJSONL 把 Replay 的结果写成 python/backtest 认得的 JSONL 格式：
// 一行 meta，随后每根K线一行 decision。
func WriteJSONL(w io.Writer, meta Meta, decisions []DecisionLine) error {
	bw := bufio.NewWriterSize(w, 1<<20)
	enc := json.NewEncoder(bw)
	if err := enc.Encode(meta); err != nil {
		return err
	}
	for _, d := range decisions {
		if err := enc.Encode(d); err != nil {
			return err
		}
	}
	return bw.Flush()
}
