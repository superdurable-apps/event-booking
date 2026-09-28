package process

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/superdurable/dex/sdk-go/dex"
)

type CapacityInput struct {
	Capacity int64 `json:"capacity"`
}

type Reservation struct {
	RegistrationID string    `json:"registrationId"`
	ReservedAt     time.Time `json:"reservedAt"`
}

type ReserveCapacityInput struct {
	RegistrationID string `json:"registrationId"`
}

type ReserveCapacityResult struct {
	Accepted      bool  `json:"accepted"`
	Duplicate     bool  `json:"duplicate"`
	ReservedCount int64 `json:"reservedCount"`
	Capacity      int64 `json:"capacity"`
}

type ReleaseCapacityResult struct {
	Released      bool  `json:"released"`
	ReservedCount int64 `json:"reservedCount"`
}

type CapacitySnapshot struct {
	Capacity      int64 `json:"capacity"`
	ReservedCount int64 `json:"reservedCount"`
}

var (
	EventCapacity      = dex.DefineAttribute[int64]("event-capacity")
	EventReservedCount = dex.DefineAttribute[int64]("event-reserved-count")
	EventReservations  = dex.DefineAttributeMap[Reservation]("event-reservations")
	CapacityControl    = dex.DefineChannel[bool]("event-capacity-control")
)

// dex:group group-id:capacity group-label:"Event capacity"
// dex:explanation text:"Initialize the durable event capacity before accepting reservations."
type InitializeCapacity struct {
	dex.StepDefaultsNoWaitFor[CapacityInput]
}

func (InitializeCapacity) Execute(ctx dex.Context, input CapacityInput) (*dex.StepDecision, error) {
	if input.Capacity < 1 {
		return nil, fmt.Errorf("capacity must be positive")
	}
	if err := EventCapacity.Set(ctx, input.Capacity); err != nil {
		return nil, err
	}
	if err := EventReservedCount.Set(ctx, 0); err != nil {
		return nil, err
	}
	return dex.GoTo(WaitForCapacityControl{}, input), nil
}

// dex:group group-id:capacity group-label:"Event capacity"
// dex:explanation text:"Keep the shared capacity Flow active while registrations reserve and release places."
type WaitForCapacityControl struct{ dex.StepDefaults }

func (WaitForCapacityControl) WaitFor(dex.Context, CapacityInput) (*dex.Wait, error) {
	return dex.AnyOf(CapacityControl.ForOne()), nil
}

func (WaitForCapacityControl) Execute(ctx dex.Context, input CapacityInput) (*dex.StepDecision, error) {
	messages, err := CapacityControl.GetConditionResults(ctx)
	if err != nil {
		return nil, err
	}
	if len(messages) == 1 && messages[0] {
		return dex.GracefulComplete(CapacitySnapshot{Capacity: input.Capacity}), nil
	}
	return dex.GoTo(WaitForCapacityControl{}, input), nil
}

type CapacityFlow struct{ dex.FlowDefaults }

func (CapacityFlow) GetSteps() []dex.StepDef {
	return []dex.StepDef{dex.DefineStartStep(InitializeCapacity{}), dex.DefineStep(WaitForCapacityControl{})}
}

func (flow CapacityFlow) GetRPCs() []dex.RPCDef {
	return []dex.RPCDef{
		dex.DefineRPC(flow.GetDexSummary, nil),
		dex.DefineRPC(flow.GetDexDisplay, nil),
		dex.DefineRPC(flow.DescribeCapacity, &dex.RPCOptions{}),
		dex.DefineRPC(flow.ReserveCapacity, &dex.RPCOptions{
			IsTransactional: true,
			LockAttributes:  []dex.AttributeLock{dex.LockAttribute(EventReservedCount), dex.LockAttribute(EventCapacity)},
		}),
		dex.DefineRPC(flow.ReleaseCapacity, &dex.RPCOptions{
			IsTransactional: true,
			LockAttributes:  []dex.AttributeLock{dex.LockAttribute(EventReservedCount)},
		}),
	}
}

func (CapacityFlow) GetPersistenceSchema() dex.PersistenceSchema {
	return dex.PersistenceSchema{
		Attributes: []dex.AttributeDef{EventCapacity, EventReservedCount, EventReservations},
		Channels:   []dex.ChannelDef{CapacityControl},
	}
}

func (CapacityFlow) DescribeCapacity(ctx dex.Context, _ dex.None) (*dex.RPCResult[CapacitySnapshot], error) {
	capacity, err := EventCapacity.Get(ctx)
	if err != nil {
		return nil, err
	}
	count, err := EventReservedCount.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &dex.RPCResult[CapacitySnapshot]{Output: CapacitySnapshot{Capacity: capacity, ReservedCount: count}}, nil
}

// dex:field attribute-key:event-capacity value-type:int64 editable:false description:"Maximum event attendance"
// dex:field attribute-key:event-reserved-count value-type:int64 editable:false description:"Current active reservations"
func (CapacityFlow) GetDexSummary(ctx dex.Context, _ dex.None) (*dex.RPCResult[map[string]any], error) {
	capacity, err := EventCapacity.Get(ctx)
	if err != nil {
		return nil, err
	}
	count, err := EventReservedCount.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &dex.RPCResult[map[string]any]{Output: map[string]any{
		"event-capacity": capacity, "event-reserved-count": count,
	}}, nil
}

// dex:field attribute-key:event-capacity value-type:int64 editable:false description:"Maximum event attendance"
// dex:field attribute-key:event-reserved-count value-type:int64 editable:false description:"Current active reservations"
func (CapacityFlow) GetDexDisplay(ctx dex.Context, _ dex.None) (*dex.RPCResult[map[string]any], error) {
	capacity, err := EventCapacity.Get(ctx)
	if err != nil {
		return nil, err
	}
	count, err := EventReservedCount.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &dex.RPCResult[map[string]any]{Output: map[string]any{
		"event-capacity": capacity, "event-reserved-count": count,
	}}, nil
}

func (CapacityFlow) ReserveCapacity(ctx dex.Context, input ReserveCapacityInput) (*dex.RPCResult[ReserveCapacityResult], error) {
	if input.RegistrationID == "" {
		return nil, fmt.Errorf("registration ID is required")
	}
	capacity, err := EventCapacity.Get(ctx)
	if err != nil {
		return nil, err
	}
	count, err := EventReservedCount.Get(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := EventReservations.Get(ctx, input.RegistrationID); err == nil {
		return &dex.RPCResult[ReserveCapacityResult]{Output: ReserveCapacityResult{
			Accepted: true, Duplicate: true, ReservedCount: count, Capacity: capacity,
		}}, nil
	} else {
		var missing *dex.AttributeNotFoundError
		if !errors.As(err, &missing) {
			return nil, err
		}
	}
	if count >= capacity {
		return &dex.RPCResult[ReserveCapacityResult]{Output: ReserveCapacityResult{
			Accepted: false, ReservedCount: count, Capacity: capacity,
		}}, nil
	}
	if err := EventReservations.Set(ctx, input.RegistrationID, Reservation{
		RegistrationID: input.RegistrationID, ReservedAt: ctx.FirstAttemptAt().UTC(),
	}); err != nil {
		return nil, err
	}
	count++
	if err := EventReservedCount.Set(ctx, count); err != nil {
		return nil, err
	}
	return &dex.RPCResult[ReserveCapacityResult]{Output: ReserveCapacityResult{
		Accepted: true, ReservedCount: count, Capacity: capacity,
	}}, nil
}

func (CapacityFlow) ReleaseCapacity(ctx dex.Context, input ReserveCapacityInput) (*dex.RPCResult[ReleaseCapacityResult], error) {
	count, err := EventReservedCount.Get(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := EventReservations.Get(ctx, input.RegistrationID); err != nil {
		var missing *dex.AttributeNotFoundError
		if errors.As(err, &missing) {
			return &dex.RPCResult[ReleaseCapacityResult]{Output: ReleaseCapacityResult{ReservedCount: count}}, nil
		}
		return nil, err
	}
	if err := EventReservations.Delete(ctx, input.RegistrationID); err != nil {
		return nil, err
	}
	if count > 0 {
		count--
	}
	if err := EventReservedCount.Set(ctx, count); err != nil {
		return nil, err
	}
	return &dex.RPCResult[ReleaseCapacityResult]{Output: ReleaseCapacityResult{Released: true, ReservedCount: count}}, nil
}

var EventCapacityFlow = CapacityFlow{}

// CapacityClient invokes the shared capacity Flow from registration Steps and HTTP handlers.
type CapacityClient struct {
	mu     sync.RWMutex
	client *dex.Client
	config EventConfig
	flow   CapacityFlow
}

func NewCapacityClient(config EventConfig) *CapacityClient {
	return &CapacityClient{config: config, flow: EventCapacityFlow}
}

func (client *CapacityClient) Attach(dexClient *dex.Client) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.client = dexClient
}

func (client *CapacityClient) dexClient() (*dex.Client, error) {
	client.mu.RLock()
	defer client.mu.RUnlock()
	if client.client == nil {
		return nil, fmt.Errorf("capacity client is not attached")
	}
	return client.client, nil
}

func (client *CapacityClient) Start(ctx context.Context) error {
	dexClient, err := client.dexClient()
	if err != nil {
		return err
	}
	_, err = dexClient.StartFlow(ctx, client.flow, client.config.CapacityFlowID(), CapacityInput{Capacity: client.config.Capacity}, dex.StartFlowOptions{IDReusePolicy: dex.IDReuseDisallow})
	var alreadyStarted *dex.FlowAlreadyStartedError
	if err != nil && !errors.As(err, &alreadyStarted) {
		return err
	}
	return client.waitUntilReady(ctx)
}

func (client *CapacityClient) waitUntilReady(ctx context.Context) error {
	readyContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var lastErr error
	for {
		if _, err := client.Get(readyContext); err == nil {
			return nil
		} else {
			lastErr = err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-readyContext.Done():
			timer.Stop()
			return fmt.Errorf("wait for event capacity Flow readiness: %w", errors.Join(readyContext.Err(), lastErr))
		case <-timer.C:
		}
	}
}

func (client *CapacityClient) Reserve(ctx context.Context, registrationID string) (ReserveCapacityResult, error) {
	dexClient, err := client.dexClient()
	if err != nil {
		return ReserveCapacityResult{}, err
	}
	var output ReserveCapacityResult
	err = dexClient.InvokeRPCWithOptions(ctx, client.config.CapacityFlowID(), client.flow.ReserveCapacity,
		ReserveCapacityInput{RegistrationID: registrationID}, &output, capacityRPCOptions(registrationID))
	return output, err
}

func (client *CapacityClient) Release(ctx context.Context, registrationID string) (ReleaseCapacityResult, error) {
	dexClient, err := client.dexClient()
	if err != nil {
		return ReleaseCapacityResult{}, err
	}
	var output ReleaseCapacityResult
	err = dexClient.InvokeRPCWithOptions(ctx, client.config.CapacityFlowID(), client.flow.ReleaseCapacity,
		ReserveCapacityInput{RegistrationID: registrationID}, &output, capacityRPCOptions(registrationID))
	return output, err
}

func (client *CapacityClient) Get(ctx context.Context) (CapacitySnapshot, error) {
	dexClient, err := client.dexClient()
	if err != nil {
		return CapacitySnapshot{}, err
	}
	var output CapacitySnapshot
	err = dexClient.InvokeRPC(ctx, client.config.CapacityFlowID(), client.flow.DescribeCapacity, nil, &output)
	return output, err
}

func capacityRPCOptions(registrationID string) dex.RPCInvokeOptions {
	return dex.RPCInvokeOptions{
		LockAttributeMapInstances: []dex.AttributeLock{dex.LockAttributeMap(EventReservations, registrationID)},
		LoadAttributeMapInstances: []dex.AttributeMapLoad{EventReservations.Load(registrationID)},
	}
}

var _ dex.Flow = EventCapacityFlow
var _ dex.Step[CapacityInput] = InitializeCapacity{}
var _ dex.Step[CapacityInput] = WaitForCapacityControl{}
var _ dex.RPC[dex.None, CapacitySnapshot] = EventCapacityFlow.DescribeCapacity
var _ dex.RPC[ReserveCapacityInput, ReserveCapacityResult] = EventCapacityFlow.ReserveCapacity
var _ dex.RPC[ReserveCapacityInput, ReleaseCapacityResult] = EventCapacityFlow.ReleaseCapacity
