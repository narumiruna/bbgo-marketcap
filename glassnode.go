package marketcap

import (
	"context"
	"time"

	"github.com/c9s/bbgo/pkg/glassnode"
)

type Glassnode struct {
	client *glassnode.RestClient
}

func NewGlassnode(apiKey string) *Glassnode {
	client := glassnode.NewClient()
	client.Auth(apiKey)
	return &Glassnode{client: client}
}

func (g *Glassnode) GetMarketCapInUSD(ctx context.Context, asset string) (float64, error) {

	req := glassnode.MarketRequest{
		Client: g.client,
		Asset:  asset,
		// 24 hours and 30 minutes ago
		Since:    time.Now().Add(-24*time.Hour - 30*time.Minute).Unix(),
		Interval: glassnode.Interval24h,
		Metric:   "marketcap_usd",
	}

	resp, err := req.Do(ctx)
	if err != nil {
		return 0, err
	}

	return resp.Last().Value, nil
}
