package backtest

import (
	"bufio"
	"encoding/json"
	"io"
)

// WriteJSONL writes a Replay result in the JSONL format python/backtest
// understands: one meta line, followed by one decision line per candle.
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
