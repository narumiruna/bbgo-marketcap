package marketcap

import (
	"context"
	"fmt"
	"math"
	"os"

	"github.com/sirupsen/logrus"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/datasource/glassnode"
	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

const ID = "marketcap"

var log = logrus.WithField("strategy", ID)

func init() {
	bbgo.RegisterStrategy(ID, &Strategy{})
}

type Strategy struct {
	Notifiability *bbgo.Notifiability
	glassnode     *glassnode.DataSource

	Interval         types.Interval   `json:"interval"`
	BaseCurrency     string           `json:"baseCurrency"`
	TargetCurrencies []string         `json:"targetCurrencies"`
	Threshold        fixedpoint.Value `json:"threshold"`
	IgnoreLocked     bool             `json:"ignoreLocked"`
	Verbose          bool             `json:"verbose"`
	DryRun           bool             `json:"dryRun"`
	// max amount to buy or sell per order
	MaxAmount fixedpoint.Value `json:"maxAmount"`
}

func (s *Strategy) Initialize() error {
	apiKey := os.Getenv("GLASSNODE_API_KEY")
	s.glassnode = glassnode.New(apiKey)
	return nil
}

func (s *Strategy) ID() string {
	return ID
}

func (s *Strategy) Validate() error {
	if len(s.TargetCurrencies) == 0 {
		return fmt.Errorf("taretCurrencies should not be empty")
	}

	for _, c := range s.TargetCurrencies {
		if c == s.BaseCurrency {
			return fmt.Errorf("targetCurrencies contain baseCurrency")
		}
	}

	if s.Threshold < 0 {
		return fmt.Errorf("threshold should not less than 0")
	}

	if s.MaxAmount.Sign() < 0 {
		return fmt.Errorf("maxAmount shoud not less than 0")
	}

	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	for _, symbol := range s.getSymbols() {
		session.Subscribe(types.KLineChannel, symbol, types.SubscribeOptions{Interval: s.Interval.String()})
	}
}

func (s *Strategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	session.MarketDataStream.OnKLineClosed(func(kline types.KLine) {
		err := s.rebalance(ctx, orderExecutor, session)
		if err != nil {
			log.WithError(err)
		}
	})
	return nil
}

func (s *Strategy) getTargetWeights(ctx context.Context) (weights types.Float64Slice, err error) {
	// get market cap values
	for _, currency := range s.TargetCurrencies {
		marketCap, err := s.glassnode.QueryMarketCapInUSD(ctx, currency)
		if err != nil {
			return nil, err
		}
		weights = append(weights, marketCap)
	}

	// normalize
	weights = weights.Normalize()

	return weights, nil
}

func (s *Strategy) rebalance(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	prices, err := s.getPrices(ctx, session)
	if err != nil {
		return err
	}

	targetWeights, err := s.getTargetWeights(ctx)
	if err != nil {
		return err
	}

	balances := session.Account.Balances()
	quantities := s.getQuantities(balances)
	marketValues := prices.Mul(quantities)

	orders := s.generateSubmitOrders(prices, marketValues, targetWeights)
	for _, order := range orders {
		log.Infof("generated submit order: %s", order.String())
	}

	if s.DryRun {
		return nil
	}

	_, err = orderExecutor.SubmitOrders(ctx, orders...)
	if err != nil {
		return err
	}

	return nil
}

func (s *Strategy) getPrices(ctx context.Context, session *bbgo.ExchangeSession) (types.Float64Slice, error) {
	var prices types.Float64Slice

	for _, currency := range s.TargetCurrencies {
		symbol := currency + s.BaseCurrency
		ticker, err := session.Exchange.QueryTicker(ctx, symbol)
		if err != nil {
			return prices, err
		}
		prices = append(prices, ticker.Last.Float64())
	}
	return prices, nil
}

func (s *Strategy) getQuantities(balances types.BalanceMap) (quantities types.Float64Slice) {
	for _, currency := range s.TargetCurrencies {
		if s.IgnoreLocked {
			quantities = append(quantities, balances[currency].Total().Float64())
		} else {
			quantities = append(quantities, balances[currency].Available.Float64())
		}
	}
	return quantities
}

func (s *Strategy) generateSubmitOrders(prices, marketValues, targetWeights types.Float64Slice) (submitOrders []types.SubmitOrder) {
	currentWeights := marketValues.Normalize()
	totalValue := marketValues.Sum()

	for i, currency := range s.TargetCurrencies {
		symbol := currency + s.BaseCurrency
		currentWeight := currentWeights[i]
		currentPrice := prices[i]
		targetWeight := targetWeights[i]

		log.Infof("%s price: %v, current weight: %v, target weight: %v",
			symbol,
			currentPrice,
			currentWeight,
			targetWeight)

		// calculate the difference between current weight and target weight
		// if the difference is less than threshold, then we will not create the order
		weightDifference := targetWeight - currentWeight
		if math.Abs(weightDifference) < s.Threshold.Float64() {
			log.Infof("%s weight distance |%v - %v| = |%v| less than the threshold: %v",
				symbol,
				currentWeight,
				targetWeight,
				weightDifference,
				s.Threshold)
			continue
		}

		quantity := fixedpoint.NewFromFloat((weightDifference * totalValue) / currentPrice)

		side := types.SideTypeBuy
		if quantity.Sign() < 0 {
			side = types.SideTypeSell
			quantity = quantity.Abs()
		}

		if s.MaxAmount.Sign() > 0 {
			quantity = bbgo.AdjustQuantityByMaxAmount(quantity, fixedpoint.NewFromFloat(currentPrice), s.MaxAmount)
			log.Infof("adjust the quantity %v (%s %s @ %v) by max amount %v",
				quantity,
				symbol,
				side.String(),
				currentPrice,
				s.MaxAmount)
		}

		order := types.SubmitOrder{
			Symbol:   symbol,
			Side:     side,
			Type:     types.OrderTypeMarket,
			Quantity: quantity}

		submitOrders = append(submitOrders, order)
	}
	return submitOrders
}

func (s *Strategy) getSymbols() (symbols []string) {
	for _, currency := range s.TargetCurrencies {
		symbol := currency + s.BaseCurrency
		symbols = append(symbols, symbol)
	}
	return symbols
}

func (s *Strategy) logTargetWeights(weights types.Float64Slice) {
	symbols := s.getSymbols()
	for i, weight := range weights {
		log.Infof("symbol: %v, target weight: %v", symbols[i], weight)
	}
}
