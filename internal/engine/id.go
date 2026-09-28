package engine

import (
	"github.com/shopspring/decimal"

	"tradeforge/pkg/idgen"
)

func newUUID() string { return idgen.NewUUID() }

func decimalZero() decimal.Decimal { return decimal.Zero }
