package process

import (
	"fmt"
	"strings"
	"time"

	"github.com/superdurable/dex/sdk-go/dex"
)

const EventFlowID = "event-booking-default"

type EventConfig struct {
	EventID          string    `json:"eventId"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	StartsAt         time.Time `json:"startsAt"`
	Timezone         string    `json:"timezone"`
	Location         string    `json:"location"`
	Currency         string    `json:"currency"`
	PriceMinor       int64     `json:"priceMinor"`
	Capacity         int64     `json:"capacity"`
	RegistrationOpen bool      `json:"registrationOpen"`
}

type ReservationState string

const (
	ReservationHeld     ReservationState = "held"
	ReservationPaid     ReservationState = "paid"
	ReservationReleased ReservationState = "released"
)

type Reservation struct {
	RegistrationID string           `json:"registrationId"`
	State          ReservationState `json:"state"`
	ReservedAt     time.Time        `json:"reservedAt"`
	UpdatedAt      time.Time        `json:"updatedAt"`
}

type EventState struct {
	Config       EventConfig            `json:"config"`
	Reservations map[string]Reservation `json:"reservations"`
	Reserved     int64                  `json:"reserved"`
	Paid         int64                  `json:"paid"`
}

type EventView struct {
	EventConfig
	Reserved  int64 `json:"reserved"`
	Paid      int64 `json:"paid"`
	Remaining int64 `json:"remaining"`
}

type ReserveSeatInput struct {
	RegistrationID string    `json:"registrationId"`
	ReservedAt     time.Time `json:"reservedAt"`
}

type ReserveSeatResult struct {
	Accepted  bool      `json:"accepted"`
	Duplicate bool      `json:"duplicate"`
	Event     EventView `json:"event"`
}

type RegistrationReference struct {
	RegistrationID string    `json:"registrationId"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

var (
	// dex:indexed-attribute attribute-key:event-state index-key:event-state index-type:keyword value-type:string description:"Event registration state"
	EventStatus = dex.DefineAttribute[string]("event-state", dex.Indexed(dex.AttributeIndex{Type: dex.IndexKeyword}))
	// dex:indexed-attribute attribute-key:event-name index-key:event-name index-type:fulltext value-type:string description:"Event name"
	EventName         = dex.DefineAttribute[string]("event-name", dex.Indexed(dex.AttributeIndex{Type: dex.IndexFullText}))
	EventData         = dex.DefineAttribute[EventState]("event-data")
	EventAvailability = dex.DefineAttribute[string]("event-availability-summary")
	EventHold         = dex.DefineChannel[bool]("event-hold")
)

// dex:group group-id:event group-label:"Event inventory"
// dex:explanation text:"Initialize the durable event configuration and its seat ledger."
type InitializeEvent struct {
	dex.StepDefaultsNoWaitFor[EventConfig]
}

func (InitializeEvent) Execute(ctx dex.Context, input EventConfig) (*dex.StepDecision, error) {
	if err := validateEventConfig(input, 0); err != nil {
		return nil, err
	}
	if err := EventName.Set(ctx, input.Name); err != nil {
		return nil, err
	}
	if err := EventStatus.Set(ctx, registrationState(input.RegistrationOpen)); err != nil {
		return nil, err
	}
	state := EventState{Config: input, Reservations: map[string]Reservation{}}
	if err := EventData.Set(ctx, state); err != nil {
		return nil, err
	}
	if err := EventAvailability.Set(ctx, availabilitySummary(state)); err != nil {
		return nil, err
	}
	return dex.GoTo(KeepEventActive{}, input), nil
}

// dex:group group-id:event group-label:"Event inventory"
// dex:explanation text:"Keep the event active while registrations reserve, pay for, and release seats."
type KeepEventActive struct{ dex.StepDefaults }

func (KeepEventActive) WaitFor(_ dex.Context, _ EventConfig) (*dex.Wait, error) {
	return dex.AnyOf(EventHold.ForOne()), nil
}

func (KeepEventActive) Execute(_ dex.Context, input EventConfig) (*dex.StepDecision, error) {
	return dex.GoTo(KeepEventActive{}, input), nil
}

type EventInventoryFlow struct{ dex.FlowDefaults }

func (EventInventoryFlow) GetSteps() []dex.StepDef {
	return []dex.StepDef{
		dex.DefineStartStep(InitializeEvent{}),
		dex.DefineStep(KeepEventActive{}),
	}
}

func (flow EventInventoryFlow) GetRPCs() []dex.RPCDef {
	lock := []dex.AttributeLock{dex.LockAttribute(EventData), dex.LockAttribute(EventStatus), dex.LockAttribute(EventName), dex.LockAttribute(EventAvailability)}
	return []dex.RPCDef{
		dex.DefineRPC(flow.GetDexSummary, nil),
		dex.DefineRPC(flow.GetDexDisplay, nil),
		dex.DefineRPC(flow.DescribeEvent, &dex.RPCOptions{LockAttributes: []dex.AttributeLock{dex.LockAttribute(EventData)}}),
		dex.DefineRPC(flow.ReserveSeat, &dex.RPCOptions{IsTransactional: true, LockAttributes: lock}),
		dex.DefineRPC(flow.ReleaseSeat, &dex.RPCOptions{IsTransactional: true, LockAttributes: lock}),
		dex.DefineRPC(flow.MarkSeatPaid, &dex.RPCOptions{IsTransactional: true, LockAttributes: lock}),
		dex.DefineRPC(flow.OpenRegistration, &dex.RPCOptions{
			Action:          dex.DefineAction("Open registration", dex.WhenAttributeMatches(EventStatus, dex.AttributeMatchEqual("closed")), dex.ActionRequiresPermission("event.manage")),
			IsTransactional: true, LockAttributes: lock,
		}),
		dex.DefineRPC(flow.CloseRegistration, &dex.RPCOptions{
			Action:          dex.DefineAction("Close registration", dex.WhenAttributeMatches(EventStatus, dex.AttributeMatchEqual("open")), dex.ActionRequiresPermission("event.manage")),
			IsTransactional: true, LockAttributes: lock,
		}),
	}
}

func (EventInventoryFlow) GetPersistenceSchema() dex.PersistenceSchema {
	return dex.PersistenceSchema{
		Attributes: []dex.AttributeDef{EventStatus, EventName, EventData, EventAvailability},
		Channels:   []dex.ChannelDef{EventHold},
	}
}

// dex:field attribute-key:event-availability-summary value-type:string editable:false description:"Current paid, held, and remaining seat totals"
func (EventInventoryFlow) GetDexSummary(ctx dex.Context, _ dex.None) (*dex.RPCResult[map[string]any], error) {
	availability, err := EventAvailability.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &dex.RPCResult[map[string]any]{Output: map[string]any{"event-availability-summary": availability}}, nil
}

// dex:field attribute-key:event-name value-type:string editable:false description:"Event name"
// dex:field attribute-key:event-state value-type:string editable:false description:"Registration state"
// dex:field attribute-key:event-data value-type:json editable:false description:"Configuration, availability, and reservation ledger"
func (EventInventoryFlow) GetDexDisplay(ctx dex.Context, _ dex.None) (*dex.RPCResult[map[string]any], error) {
	name, err := EventName.Get(ctx)
	if err != nil {
		return nil, err
	}
	status, err := EventStatus.Get(ctx)
	if err != nil {
		return nil, err
	}
	state, err := EventData.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &dex.RPCResult[map[string]any]{Output: map[string]any{"event-name": name, "event-state": status, "event-data": state}}, nil
}

func (EventInventoryFlow) DescribeEvent(ctx dex.Context, _ dex.None) (*dex.RPCResult[EventView], error) {
	state, err := EventData.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &dex.RPCResult[EventView]{Output: eventView(state)}, nil
}

func (EventInventoryFlow) ReserveSeat(ctx dex.Context, input ReserveSeatInput) (*dex.RPCResult[ReserveSeatResult], error) {
	state, err := EventData.Get(ctx)
	if err != nil {
		return nil, err
	}
	registrationID := strings.TrimSpace(input.RegistrationID)
	if registrationID == "" {
		return nil, fmt.Errorf("registration ID is required")
	}
	if existing, ok := state.Reservations[registrationID]; ok && existing.State != ReservationReleased {
		return &dex.RPCResult[ReserveSeatResult]{Output: ReserveSeatResult{Accepted: true, Duplicate: true, Event: eventView(state)}}, nil
	}
	if !state.Config.RegistrationOpen || state.Reserved+state.Paid >= state.Config.Capacity {
		return &dex.RPCResult[ReserveSeatResult]{Output: ReserveSeatResult{Event: eventView(state)}}, nil
	}
	now := input.ReservedAt.UTC()
	if now.IsZero() {
		now = time.Unix(0, 0).UTC()
	}
	state.Reservations[registrationID] = Reservation{RegistrationID: registrationID, State: ReservationHeld, ReservedAt: now, UpdatedAt: now}
	state.Reserved++
	if err := EventData.Set(ctx, state); err != nil {
		return nil, err
	}
	if err := EventAvailability.Set(ctx, availabilitySummary(state)); err != nil {
		return nil, err
	}
	return &dex.RPCResult[ReserveSeatResult]{Output: ReserveSeatResult{Accepted: true, Event: eventView(state)}}, nil
}

func (EventInventoryFlow) ReleaseSeat(ctx dex.Context, input RegistrationReference) (*dex.RPCResult[EventView], error) {
	state, err := EventData.Get(ctx)
	if err != nil {
		return nil, err
	}
	reservation, ok := state.Reservations[input.RegistrationID]
	if ok && reservation.State == ReservationHeld {
		reservation.State = ReservationReleased
		reservation.UpdatedAt = input.UpdatedAt.UTC()
		state.Reservations[input.RegistrationID] = reservation
		state.Reserved--
		if err := EventData.Set(ctx, state); err != nil {
			return nil, err
		}
		if err := EventAvailability.Set(ctx, availabilitySummary(state)); err != nil {
			return nil, err
		}
	}
	return &dex.RPCResult[EventView]{Output: eventView(state)}, nil
}

func (EventInventoryFlow) MarkSeatPaid(ctx dex.Context, input RegistrationReference) (*dex.RPCResult[EventView], error) {
	state, err := EventData.Get(ctx)
	if err != nil {
		return nil, err
	}
	reservation, ok := state.Reservations[input.RegistrationID]
	if !ok || reservation.State == ReservationReleased {
		return nil, fmt.Errorf("registration does not hold a seat")
	}
	if reservation.State == ReservationHeld {
		reservation.State = ReservationPaid
		reservation.UpdatedAt = input.UpdatedAt.UTC()
		state.Reservations[input.RegistrationID] = reservation
		state.Reserved--
		state.Paid++
		if err := EventData.Set(ctx, state); err != nil {
			return nil, err
		}
		if err := EventAvailability.Set(ctx, availabilitySummary(state)); err != nil {
			return nil, err
		}
	}
	return &dex.RPCResult[EventView]{Output: eventView(state)}, nil
}

func (EventInventoryFlow) OpenRegistration(ctx dex.Context, _ dex.None) (*dex.RPCResult[dex.None], error) {
	return setRegistrationOpen(ctx, true)
}

func (EventInventoryFlow) CloseRegistration(ctx dex.Context, _ dex.None) (*dex.RPCResult[dex.None], error) {
	return setRegistrationOpen(ctx, false)
}

func setRegistrationOpen(ctx dex.Context, open bool) (*dex.RPCResult[dex.None], error) {
	state, err := EventData.Get(ctx)
	if err != nil {
		return nil, err
	}
	state.Config.RegistrationOpen = open
	if err := EventData.Set(ctx, state); err != nil {
		return nil, err
	}
	if err := EventStatus.Set(ctx, registrationState(open)); err != nil {
		return nil, err
	}
	return &dex.RPCResult[dex.None]{}, nil
}

func registrationState(open bool) string {
	if open {
		return "open"
	}
	return "closed"
}

func validateEventConfig(input EventConfig, seatsInUse int64) error {
	if strings.TrimSpace(input.EventID) == "" || strings.TrimSpace(input.Name) == "" {
		return fmt.Errorf("event ID and name are required")
	}
	if input.Capacity < 1 || input.PriceMinor < 1 {
		return fmt.Errorf("event capacity and price must be positive")
	}
	if input.Capacity < seatsInUse {
		return fmt.Errorf("event capacity cannot be lower than seats already in use")
	}
	if len(strings.TrimSpace(input.Currency)) != 3 {
		return fmt.Errorf("event currency must use a three-letter code")
	}
	return nil
}

func eventView(state EventState) EventView {
	remaining := state.Config.Capacity - state.Reserved - state.Paid
	if remaining < 0 {
		remaining = 0
	}
	return EventView{EventConfig: state.Config, Reserved: state.Reserved, Paid: state.Paid, Remaining: remaining}
}

func availabilitySummary(state EventState) string {
	view := eventView(state)
	return fmt.Sprintf("%d paid · %d held · %d remaining", view.Paid, view.Reserved, view.Remaining)
}

var EventInventory = EventInventoryFlow{}

var _ dex.Flow = EventInventory
var _ dex.Step[EventConfig] = InitializeEvent{}
var _ dex.Step[EventConfig] = KeepEventActive{}
var _ dex.RPC[dex.None, map[string]any] = EventInventory.GetDexSummary
var _ dex.RPC[dex.None, map[string]any] = EventInventory.GetDexDisplay
var _ dex.RPC[dex.None, EventView] = EventInventory.DescribeEvent
var _ dex.RPC[ReserveSeatInput, ReserveSeatResult] = EventInventory.ReserveSeat
var _ dex.RPC[RegistrationReference, EventView] = EventInventory.ReleaseSeat
var _ dex.RPC[RegistrationReference, EventView] = EventInventory.MarkSeatPaid
var _ dex.RPC[dex.None, dex.None] = EventInventory.OpenRegistration
var _ dex.RPC[dex.None, dex.None] = EventInventory.CloseRegistration
