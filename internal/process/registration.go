package process

import (
	"fmt"
	"html"
	"strings"
	"time"

	gmail "github.com/superdurable/dex-connectors-library/connectors/google/gmail"
	stripe "github.com/superdurable/dex-connectors-library/connectors/stripe"
	"github.com/superdurable/dex-connectors-library/sdkgo"
	"github.com/superdurable/dex/sdk-go/dex"
)

const (
	StripeConnectionName         = "event-payments"
	GmailConnectionName          = "event-tickets"
	StripeTriggerBindingName     = "event-registration-payments"
	CreateCheckoutStepType       = "CreateACHCheckout"
	ReconcileCheckoutStepType    = "ReconcileACHCheckout"
	SendTicketEmailStepType      = "SendTicketEmail"
	maximumProcessedStripeEvents = 32
)

type RegistrationStatus string

const (
	StatusStarting       RegistrationStatus = "starting"
	StatusReserved       RegistrationStatus = "reserved"
	StatusCheckoutReady  RegistrationStatus = "checkout_ready"
	StatusPaymentPending RegistrationStatus = "payment_pending"
	StatusPaid           RegistrationStatus = "paid"
	StatusTicketEmailed  RegistrationStatus = "ticket_emailed"
	StatusSoldOut        RegistrationStatus = "sold_out"
	StatusPaymentFailed  RegistrationStatus = "payment_failed"
	StatusExpired        RegistrationStatus = "expired"
	StatusCancelled      RegistrationStatus = "cancelled"
	StatusNeedsAttention RegistrationStatus = "needs_attention"
)

type RegistrationInput struct {
	RegistrationID    string    `json:"registrationId"`
	RegistrationToken string    `json:"registrationToken"`
	TicketToken       string    `json:"ticketToken"`
	FirstName         string    `json:"firstName"`
	LastName          string    `json:"lastName"`
	Email             string    `json:"email"`
	AcceptedTermsAt   time.Time `json:"acceptedTermsAt"`
	CreatedAt         time.Time `json:"createdAt"`
	PublicBaseURL     string    `json:"publicBaseUrl"`
	Event             EventView `json:"event"`
	SeatReserved      bool      `json:"seatReserved"`
}

type RegistrationState struct {
	RegistrationInput
	Status                  RegistrationStatus `json:"status"`
	CheckoutSessionID       string             `json:"checkoutSessionId,omitempty"`
	CheckoutURL             string             `json:"checkoutUrl,omitempty"`
	PaymentIntentID         string             `json:"paymentIntentId,omitempty"`
	TicketCode              string             `json:"ticketCode"`
	TicketURL               string             `json:"ticketUrl"`
	EmailMessageID          string             `json:"emailMessageId,omitempty"`
	CheckedInAt             time.Time          `json:"checkedInAt,omitempty"`
	ProcessedStripeEventIDs []string           `json:"processedStripeEventIds,omitempty"`
	Message                 string             `json:"message,omitempty"`
}

type PaymentEvent struct {
	EventID    string                 `json:"eventId"`
	Type       string                 `json:"type"`
	Session    stripe.CheckoutSession `json:"session"`
	ObservedAt time.Time              `json:"observedAt"`
}

type PaymentEventResult struct {
	Accepted  bool               `json:"accepted"`
	Duplicate bool               `json:"duplicate"`
	Status    RegistrationStatus `json:"status"`
}

type CheckInInput struct {
	ScannedAt time.Time `json:"scannedAt"`
}

type CheckInResult struct {
	Status       string    `json:"status"`
	AttendeeName string    `json:"attendeeName"`
	TicketCode   string    `json:"ticketCode"`
	CheckedInAt  time.Time `json:"checkedInAt"`
}

var (
	// dex:indexed-attribute attribute-key:registration-state index-key:registration-state index-type:keyword value-type:string description:"Registration lifecycle state"
	RegistrationStatusAttribute = dex.DefineAttribute[string]("registration-state", dex.Indexed(dex.AttributeIndex{Type: dex.IndexKeyword}))
	// dex:indexed-attribute attribute-key:ticket-code index-key:ticket-code index-type:keyword value-type:string description:"Opaque ticket code"
	TicketCodeAttribute  = dex.DefineAttribute[string]("ticket-code", dex.Indexed(dex.AttributeIndex{Type: dex.IndexKeyword}))
	RegistrationData     = dex.DefineAttribute[RegistrationState]("registration-data")
	RegistrationSummary  = dex.DefineAttribute[string]("registration-summary")
	CreateCheckoutResult = dex.DefineAttribute[stripe.CreateACHCheckoutSessionResult]("stripe-create-checkout-result")
	ReconcileResult      = dex.DefineAttribute[stripe.GetCheckoutSessionResult]("stripe-reconcile-result")
	SendEmailResult      = dex.DefineAttribute[gmail.SendMessageResult]("gmail-send-ticket-result")
	PaymentEvents        = dex.DefineChannel[PaymentEvent]("stripe-payment-events")
	RegistrationHold     = dex.DefineChannel[bool]("registration-hold")
)

// dex:group group-id:registration group-label:"Registration"
// dex:explanation text:"Persist attendee details and initialize the durable registration state."
type InitializeRegistration struct {
	dex.StepDefaultsNoWaitFor[RegistrationInput]
}

func (InitializeRegistration) Execute(ctx dex.Context, input RegistrationInput) (*dex.StepDecision, error) {
	if err := validateRegistrationInput(input); err != nil {
		return nil, err
	}
	state := RegistrationState{
		RegistrationInput: input,
		Status:            StatusStarting,
		TicketCode:        shortTicketCode(input.RegistrationID),
		TicketURL:         strings.TrimRight(input.PublicBaseURL, "/") + "/tickets/" + input.TicketToken,
	}
	if err := setRegistrationState(ctx, state); err != nil {
		return nil, err
	}
	if err := TicketCodeAttribute.Set(ctx, state.TicketCode); err != nil {
		return nil, err
	}
	if !input.SeatReserved {
		state.Status = StatusSoldOut
		state.Message = "Registration is closed or the event is sold out."
		if err := setRegistrationState(ctx, state); err != nil {
			return nil, err
		}
		return dex.GoTo(MaintainRegistration{}, state), nil
	}
	state.Status = StatusReserved
	state.Message = "Your seat is reserved while you complete ACH payment."
	if err := setRegistrationState(ctx, state); err != nil {
		return nil, err
	}
	return dex.GoTo(sdkgo.StepRef[RegistrationInput](CreateCheckoutStepType), input), nil
}

// dex:group group-id:payment group-label:"Stripe ACH payment"
// dex:explanation text:"Store the Stripe-hosted checkout details and wait for a signed asynchronous payment event."
type CheckoutCreated struct {
	dex.StepDefaultsNoWaitFor[stripe.CreateACHCheckoutSessionResult]
}

func (CheckoutCreated) Execute(ctx dex.Context, result stripe.CreateACHCheckoutSessionResult) (*dex.StepDecision, error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	state.CheckoutSessionID = result.Value.ID
	state.CheckoutURL = result.Value.URL
	state.Status = StatusCheckoutReady
	state.Message = "Continue to Stripe to authorize payment from a US bank account."
	if err := setRegistrationState(ctx, state); err != nil {
		return nil, err
	}
	return dex.GoTo(AwaitPayment{}, state), nil
}

// dex:group group-id:payment group-label:"Stripe ACH payment"
// dex:explanation text:"Record a conclusive checkout setup failure for staff recovery."
type CheckoutSetupFailed struct {
	dex.StepDefaultsNoWaitFor[stripe.CreateACHCheckoutSessionResult]
}

func (CheckoutSetupFailed) Execute(ctx dex.Context, _ stripe.CreateACHCheckoutSessionResult) (*dex.StepDecision, error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	state.Status = StatusNeedsAttention
	state.Message = "Payment setup needs staff attention. Your seat remains reserved."
	if err := setRegistrationState(ctx, state); err != nil {
		return nil, err
	}
	return dex.GoTo(MaintainRegistration{}, state), nil
}

// dex:group group-id:payment group-label:"Stripe ACH payment"
// dex:explanation text:"Wait durably for Stripe completion, settlement, failure, or expiration."
type AwaitPayment struct{ dex.StepDefaults }

func (AwaitPayment) WaitFor(_ dex.Context, _ RegistrationState) (*dex.Wait, error) {
	return dex.AnyOf(PaymentEvents.ForOne()), nil
}

func (AwaitPayment) Execute(ctx dex.Context, _ RegistrationState) (*dex.StepDecision, error) {
	events, err := PaymentEvents.GetConditionResults(ctx)
	if err != nil {
		return nil, err
	}
	if len(events) != 1 {
		return nil, fmt.Errorf("payment wait completed without exactly one Stripe event")
	}
	event := events[0]
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	switch event.Type {
	case "checkout.session.completed":
		if event.Session.PaymentStatus == "paid" {
			return dex.GoTo(ConfirmPayment{}, event), nil
		}
		state.Status = StatusPaymentPending
		state.Message = "Your bank payment is pending settlement. We will email the ticket after it succeeds."
		if err := setRegistrationState(ctx, state); err != nil {
			return nil, err
		}
		return dex.GoTo(AwaitPayment{}, state), nil
	case "checkout.session.async_payment_succeeded":
		return dex.GoTo(ConfirmPayment{}, event), nil
	case "checkout.session.async_payment_failed":
		return dex.GoTo(ReleaseRegistration{}, releaseInput{Status: StatusPaymentFailed, Message: "The ACH payment failed. The seat has been released.", UpdatedAt: event.ObservedAt}), nil
	case "checkout.session.expired":
		return dex.GoTo(ReleaseRegistration{}, releaseInput{Status: StatusExpired, Message: "The checkout expired. The seat has been released.", UpdatedAt: event.ObservedAt}), nil
	default:
		return nil, fmt.Errorf("unsupported Stripe event type %q", event.Type)
	}
}

// dex:group group-id:payment group-label:"Stripe ACH payment"
// dex:explanation text:"Verify the paid session and atomically convert the held seat to paid."
type ConfirmPayment struct {
	dex.StepDefaultsNoWaitFor[PaymentEvent]
}

func (ConfirmPayment) Execute(ctx dex.Context, event PaymentEvent) (*dex.StepDecision, error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	if err := validatePaidSession(state, event.Session); err != nil {
		state.Status = StatusNeedsAttention
		state.Message = "Stripe reported payment, but its amount or registration reference did not match."
		if setErr := setRegistrationState(ctx, state); setErr != nil {
			return nil, setErr
		}
		return dex.GoTo(MaintainRegistration{}, state), nil
	}
	state.PaymentIntentID = event.Session.PaymentIntentID
	state.Status = StatusPaid
	state.Message = "Payment confirmed. Your ticket email is being sent."
	if err := setRegistrationState(ctx, state); err != nil {
		return nil, err
	}
	return dex.GoTo(sdkgo.StepRef[RegistrationState](SendTicketEmailStepType), state), nil
}

type releaseInput struct {
	Status    RegistrationStatus `json:"status"`
	Message   string             `json:"message"`
	UpdatedAt time.Time          `json:"updatedAt"`
}

// dex:group group-id:registration group-label:"Registration"
// dex:explanation text:"Release an unpaid seat and retain the terminal registration record for audit."
type ReleaseRegistration struct {
	dex.StepDefaultsNoWaitFor[releaseInput]
}

func (ReleaseRegistration) Execute(ctx dex.Context, input releaseInput) (*dex.StepDecision, error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	state.Status = input.Status
	state.Message = input.Message
	if err := setRegistrationState(ctx, state); err != nil {
		return nil, err
	}
	return dex.GoTo(MaintainRegistration{}, state), nil
}

// dex:group group-id:ticket group-label:"Ticket delivery"
// dex:explanation text:"Record the Gmail message ID after the private QR ticket link is sent."
type TicketEmailSent struct {
	dex.StepDefaultsNoWaitFor[gmail.SendMessageResult]
}

func (TicketEmailSent) Execute(ctx dex.Context, result gmail.SendMessageResult) (*dex.StepDecision, error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	state.EmailMessageID = result.Value.MessageID
	state.Status = StatusTicketEmailed
	state.Message = "Payment confirmed and ticket email sent."
	if err := setRegistrationState(ctx, state); err != nil {
		return nil, err
	}
	return dex.GoTo(MaintainRegistration{}, state), nil
}

// dex:group group-id:ticket group-label:"Ticket delivery"
// dex:explanation text:"Preserve the paid ticket when email delivery needs staff attention."
type TicketEmailFailed struct {
	dex.StepDefaultsNoWaitFor[gmail.SendMessageResult]
}

func (TicketEmailFailed) Execute(ctx dex.Context, _ gmail.SendMessageResult) (*dex.StepDecision, error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	state.Status = StatusNeedsAttention
	state.Message = "Payment is confirmed, but the ticket email needs staff attention."
	if err := setRegistrationState(ctx, state); err != nil {
		return nil, err
	}
	return dex.GoTo(MaintainRegistration{}, state), nil
}

// dex:group group-id:payment group-label:"Stripe ACH payment"
// dex:explanation text:"Apply a query-first Stripe reconciliation before any recovery action."
type CheckoutReconciled struct {
	dex.StepDefaultsNoWaitFor[stripe.GetCheckoutSessionResult]
}

func (CheckoutReconciled) Execute(ctx dex.Context, result stripe.GetCheckoutSessionResult) (*dex.StepDecision, error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	event := PaymentEvent{EventID: "reconcile:" + result.Value.ID, Session: result.Value, ObservedAt: result.Receipt.ObservedAt}
	if result.Value.PaymentStatus == "paid" {
		event.Type = "checkout.session.async_payment_succeeded"
		return dex.GoTo(ConfirmPayment{}, event), nil
	}
	if result.Value.Status == "expired" {
		return dex.GoTo(ReleaseRegistration{}, releaseInput{Status: StatusExpired, Message: "Stripe confirms that checkout expired. The seat has been released.", UpdatedAt: event.ObservedAt}), nil
	}
	state.Status = StatusPaymentPending
	state.Message = "Stripe confirms that ACH payment is still pending."
	if err := setRegistrationState(ctx, state); err != nil {
		return nil, err
	}
	return dex.GoTo(AwaitPayment{}, state), nil
}

// dex:group group-id:payment group-label:"Stripe ACH payment"
// dex:explanation text:"Keep an inconclusive reconciliation visible for staff follow-up."
type ReconcileFailed struct {
	dex.StepDefaultsNoWaitFor[stripe.GetCheckoutSessionResult]
}

func (ReconcileFailed) Execute(ctx dex.Context, _ stripe.GetCheckoutSessionResult) (*dex.StepDecision, error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	state.Status = StatusNeedsAttention
	state.Message = "Stripe reconciliation was inconclusive and needs staff attention."
	if err := setRegistrationState(ctx, state); err != nil {
		return nil, err
	}
	return dex.GoTo(MaintainRegistration{}, state), nil
}

// dex:group group-id:registration group-label:"Registration"
// dex:explanation text:"Keep the registration active for check-in and authorized recovery actions."
type MaintainRegistration struct{ dex.StepDefaults }

func (MaintainRegistration) WaitFor(_ dex.Context, _ RegistrationState) (*dex.Wait, error) {
	return dex.AnyOf(RegistrationHold.ForOne()), nil
}

func (MaintainRegistration) Execute(_ dex.Context, state RegistrationState) (*dex.StepDecision, error) {
	return dex.GoTo(MaintainRegistration{}, state), nil
}

type RegistrationFlow struct {
	dex.FlowDefaults
	stripeConnection stripe.Connection
	gmailConnection  gmail.Connection
}

func NewRegistrationFlow(stripeConnection stripe.Connection, gmailConnection gmail.Connection) *RegistrationFlow {
	return &RegistrationFlow{stripeConnection: stripeConnection, gmailConnection: gmailConnection}
}

func (flow *RegistrationFlow) GetSteps() []dex.StepDef {
	return []dex.StepDef{
		dex.DefineStartStep(InitializeRegistration{}),
		dex.DefineStep(stripe.NewCreateACHCheckoutSessionStep(stripe.CreateACHCheckoutSessionStepConfig[RegistrationInput]{
			StepType: CreateCheckoutStepType, Connection: flow.stripeConnection, ConnectionName: StripeConnectionName,
			Annotations:         sdkgo.StepAnnotations{GroupID: "payment", GroupLabel: "Stripe ACH payment", Explanation: "Create an idempotent Stripe-hosted Checkout Session for one US bank account payment."},
			MapToOperationInput: mapCheckoutInput,
			Created:             sdkgo.GoTo(CheckoutCreated{}), ProviderRejected: sdkgo.GoTo(CheckoutSetupFailed{}), Uncertain: sdkgo.GoTo(CheckoutSetupFailed{}),
			InvalidResponse: sdkgo.GoTo(CheckoutSetupFailed{}), Defect: sdkgo.GoTo(CheckoutSetupFailed{}), ResultAttribute: &CreateCheckoutResult,
		})),
		dex.DefineStep(CheckoutCreated{}),
		dex.DefineStep(CheckoutSetupFailed{}),
		dex.DefineStep(AwaitPayment{}),
		dex.DefineStep(ConfirmPayment{}),
		dex.DefineStep(ReleaseRegistration{}),
		dex.DefineStep(stripe.NewGetCheckoutSessionStep(stripe.GetCheckoutSessionStepConfig[RegistrationState]{
			StepType: ReconcileCheckoutStepType, Connection: flow.stripeConnection, ConnectionName: StripeConnectionName,
			Annotations: sdkgo.StepAnnotations{GroupID: "payment", GroupLabel: "Stripe ACH payment", Explanation: "Query Stripe before deciding how to recover an uncertain or delayed payment."},
			MapToOperationInput: func(state RegistrationState) stripe.GetCheckoutSessionInput {
				return stripe.GetCheckoutSessionInput{SessionID: state.CheckoutSessionID}
			},
			Found: sdkgo.GoTo(CheckoutReconciled{}), NotFound: sdkgo.GoTo(ReconcileFailed{}), ProviderRejected: sdkgo.GoTo(ReconcileFailed{}),
			InvalidResponse: sdkgo.GoTo(ReconcileFailed{}), Defect: sdkgo.GoTo(ReconcileFailed{}), ResultAttribute: &ReconcileResult,
		})),
		dex.DefineStep(CheckoutReconciled{}),
		dex.DefineStep(ReconcileFailed{}),
		dex.DefineStep(gmail.NewSendMessageStep(gmail.SendMessageStepConfig[RegistrationState]{
			StepType: SendTicketEmailStepType, Connection: flow.gmailConnection, ConnectionName: GmailConnectionName,
			Annotations:         sdkgo.StepAnnotations{GroupID: "ticket", GroupLabel: "Ticket delivery", Explanation: "Email the attendee a private ticket link after Stripe confirms payment."},
			MapToOperationInput: mapTicketEmail,
			Sent:                sdkgo.GoTo(TicketEmailSent{}), ProviderRejected: sdkgo.GoTo(TicketEmailFailed{}), Uncertain: sdkgo.GoTo(TicketEmailFailed{}), Defect: sdkgo.GoTo(TicketEmailFailed{}),
			ResultAttribute: &SendEmailResult,
		})),
		dex.DefineStep(TicketEmailSent{}),
		dex.DefineStep(TicketEmailFailed{}),
		dex.DefineStep(MaintainRegistration{}),
	}
}

func (flow *RegistrationFlow) GetRPCs() []dex.RPCDef {
	lock := []dex.AttributeLock{dex.LockAttribute(RegistrationData), dex.LockAttribute(RegistrationStatusAttribute), dex.LockAttribute(RegistrationSummary)}
	return []dex.RPCDef{
		dex.DefineRPC(flow.GetDexSummary, nil),
		dex.DefineRPC(flow.GetDexDisplay, nil),
		dex.DefineRPC(flow.DescribeRegistration, &dex.RPCOptions{LockAttributes: []dex.AttributeLock{dex.LockAttribute(RegistrationData)}}),
		dex.DefineRPC(flow.ReceiveStripeEvent, &dex.RPCOptions{IsTransactional: true, LockAttributes: lock}),
		dex.DefineRPC(flow.CheckIn, &dex.RPCOptions{IsTransactional: true, LockAttributes: lock}),
		dex.DefineRPC(flow.CancelRegistration, &dex.RPCOptions{
			Action: dex.DefineAction("Cancel unpaid registration", dex.WhenAttributeMatches(RegistrationStatusAttribute,
				dex.AttributeMatchEqual(string(StatusReserved)), dex.AttributeMatchEqual(string(StatusCheckoutReady)), dex.AttributeMatchEqual(string(StatusPaymentPending)), dex.AttributeMatchEqual(string(StatusNeedsAttention))),
				dex.ActionRequiresPermission("registration.cancel")), IsTransactional: true, LockAttributes: lock,
		}),
		dex.DefineRPC(flow.ResendTicket, &dex.RPCOptions{
			Action: dex.DefineAction("Resend ticket email", dex.WhenAttributeMatches(RegistrationStatusAttribute,
				dex.AttributeMatchEqual(string(StatusPaid)), dex.AttributeMatchEqual(string(StatusTicketEmailed)), dex.AttributeMatchEqual(string(StatusNeedsAttention))),
				dex.ActionRequiresPermission("ticket.resend")), IsTransactional: true, LockAttributes: lock,
		}),
		dex.DefineRPC(flow.ReconcilePayment, &dex.RPCOptions{
			Action: dex.DefineAction("Reconcile Stripe payment", dex.WhenAttributeMatches(RegistrationStatusAttribute,
				dex.AttributeMatchEqual(string(StatusCheckoutReady)), dex.AttributeMatchEqual(string(StatusPaymentPending)), dex.AttributeMatchEqual(string(StatusNeedsAttention))),
				dex.ActionRequiresPermission("payment.recover")), IsTransactional: true, LockAttributes: lock,
		}),
		dex.DefineRPC(flow.UndoCheckIn, &dex.RPCOptions{
			Action: dex.DefineAction("Undo check-in", dex.WhenAttributeMatches(RegistrationStatusAttribute,
				dex.AttributeMatchEqual(string(StatusPaid)), dex.AttributeMatchEqual(string(StatusTicketEmailed)), dex.AttributeMatchEqual(string(StatusNeedsAttention))),
				dex.ActionRequiresPermission("registration.recover")), IsTransactional: true, LockAttributes: lock,
		}),
	}
}

func (*RegistrationFlow) GetPersistenceSchema() dex.PersistenceSchema {
	return dex.PersistenceSchema{
		Attributes: []dex.AttributeDef{RegistrationStatusAttribute, TicketCodeAttribute, RegistrationData, RegistrationSummary, CreateCheckoutResult, ReconcileResult, SendEmailResult},
		Channels:   []dex.ChannelDef{PaymentEvents, RegistrationHold},
	}
}

func (*RegistrationFlow) GetConnectorTriggerBindings() []sdkgo.TriggerBindingDefinition {
	return []sdkgo.TriggerBindingDefinition{stripe.DefineCheckoutSessionUpdatedTriggerBinding(stripe.CheckoutSessionUpdatedTriggerBindingConfig{
		ConnectionName: StripeConnectionName,
		BindingName:    StripeTriggerBindingName,
	})}
}

// dex:field attribute-key:registration-summary value-type:string editable:false description:"Attendee and current registration status"
func (*RegistrationFlow) GetDexSummary(ctx dex.Context, _ dex.None) (*dex.RPCResult[map[string]any], error) {
	summary, err := RegistrationSummary.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &dex.RPCResult[map[string]any]{Output: map[string]any{"registration-summary": summary}}, nil
}

// dex:field attribute-key:registration-state value-type:string editable:false description:"Current lifecycle state"
// dex:field attribute-key:ticket-code value-type:string editable:false description:"Opaque ticket code"
// dex:field attribute-key:registration-data value-type:json editable:false description:"Attendee, payment, ticket, and check-in details"
// dex:field attribute-key:stripe-create-checkout-result value-type:object editable:false description:"Stripe checkout provider result"
// dex:field attribute-key:stripe-reconcile-result value-type:object editable:false description:"Latest Stripe reconciliation result"
// dex:field attribute-key:gmail-send-ticket-result value-type:object editable:false description:"Latest Gmail ticket delivery result"
func (*RegistrationFlow) GetDexDisplay(ctx dex.Context, _ dex.None) (*dex.RPCResult[map[string]any], error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	status, err := RegistrationStatusAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	code, err := TicketCodeAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	checkout, _ := CreateCheckoutResult.Get(ctx)
	reconcile, _ := ReconcileResult.Get(ctx)
	emailResult, _ := SendEmailResult.Get(ctx)
	return &dex.RPCResult[map[string]any]{Output: map[string]any{
		"registration-state": status, "ticket-code": code, "registration-data": state,
		"stripe-create-checkout-result": checkout, "stripe-reconcile-result": reconcile, "gmail-send-ticket-result": emailResult,
	}}, nil
}

func (*RegistrationFlow) DescribeRegistration(ctx dex.Context, _ dex.None) (*dex.RPCResult[RegistrationState], error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &dex.RPCResult[RegistrationState]{Output: state}, nil
}

func (*RegistrationFlow) ReceiveStripeEvent(ctx dex.Context, input PaymentEvent) (*dex.RPCResult[PaymentEventResult], error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	for _, eventID := range state.ProcessedStripeEventIDs {
		if eventID == input.EventID {
			return &dex.RPCResult[PaymentEventResult]{Output: PaymentEventResult{Duplicate: true, Status: state.Status}}, nil
		}
	}
	if input.Session.ClientReferenceID != state.RegistrationID {
		return nil, sdkgo.MarkTriggerUndeliverable(fmt.Errorf("Stripe registration reference does not match Flow"))
	}
	if state.CheckoutSessionID != "" && input.Session.ID != state.CheckoutSessionID {
		return nil, sdkgo.MarkTriggerUndeliverable(fmt.Errorf("Stripe Checkout Session does not match registration"))
	}
	state.ProcessedStripeEventIDs = append(state.ProcessedStripeEventIDs, input.EventID)
	if len(state.ProcessedStripeEventIDs) > maximumProcessedStripeEvents {
		state.ProcessedStripeEventIDs = state.ProcessedStripeEventIDs[len(state.ProcessedStripeEventIDs)-maximumProcessedStripeEvents:]
	}
	if err := RegistrationData.Set(ctx, state); err != nil {
		return nil, err
	}
	if err := PaymentEvents.Publish(ctx, input); err != nil {
		return nil, err
	}
	return &dex.RPCResult[PaymentEventResult]{Output: PaymentEventResult{Accepted: true, Status: state.Status}}, nil
}

func (*RegistrationFlow) CheckIn(ctx dex.Context, input CheckInInput) (*dex.RPCResult[CheckInResult], error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	if state.Status != StatusPaid && state.Status != StatusTicketEmailed && state.Status != StatusNeedsAttention {
		return nil, fmt.Errorf("ticket is not paid")
	}
	result := CheckInResult{AttendeeName: attendeeName(state), TicketCode: state.TicketCode}
	if !state.CheckedInAt.IsZero() {
		result.Status = "already_checked_in"
		result.CheckedInAt = state.CheckedInAt
		return &dex.RPCResult[CheckInResult]{Output: result}, nil
	}
	if input.ScannedAt.IsZero() {
		return nil, fmt.Errorf("scan timestamp is required")
	}
	state.CheckedInAt = input.ScannedAt.UTC()
	if err := setRegistrationState(ctx, state); err != nil {
		return nil, err
	}
	result.Status = "checked_in"
	result.CheckedInAt = state.CheckedInAt
	return &dex.RPCResult[CheckInResult]{Output: result}, nil
}

func (*RegistrationFlow) CancelRegistration(ctx dex.Context, _ dex.None) (*dex.RPCResult[dex.None], error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	if state.Status == StatusPaid || state.Status == StatusTicketEmailed || !state.CheckedInAt.IsZero() {
		return nil, fmt.Errorf("paid or checked-in registration cannot be cancelled here")
	}
	return &dex.RPCResult[dex.None]{NextSteps: []dex.StepMovement{dex.MovementOf(ReleaseRegistration{}, releaseInput{
		Status: StatusCancelled, Message: "Registration cancelled by staff. The seat has been released.", UpdatedAt: state.CreatedAt,
	})}}, nil
}

func (*RegistrationFlow) ResendTicket(ctx dex.Context, _ dex.None) (*dex.RPCResult[dex.None], error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	if state.PaymentIntentID == "" {
		return nil, fmt.Errorf("registration is not paid")
	}
	return &dex.RPCResult[dex.None]{NextSteps: []dex.StepMovement{dex.MovementOf(sdkgo.StepRef[RegistrationState](SendTicketEmailStepType), state)}}, nil
}

func (*RegistrationFlow) ReconcilePayment(ctx dex.Context, _ dex.None) (*dex.RPCResult[dex.None], error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	if state.CheckoutSessionID == "" {
		return nil, fmt.Errorf("Stripe Checkout Session is not ready")
	}
	return &dex.RPCResult[dex.None]{NextSteps: []dex.StepMovement{dex.MovementOf(sdkgo.StepRef[RegistrationState](ReconcileCheckoutStepType), state)}}, nil
}

func (*RegistrationFlow) UndoCheckIn(ctx dex.Context, _ dex.None) (*dex.RPCResult[dex.None], error) {
	state, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	state.CheckedInAt = time.Time{}
	if err := setRegistrationState(ctx, state); err != nil {
		return nil, err
	}
	return &dex.RPCResult[dex.None]{}, nil
}

func setRegistrationState(ctx dex.Context, state RegistrationState) error {
	if err := RegistrationData.Set(ctx, state); err != nil {
		return err
	}
	if err := RegistrationStatusAttribute.Set(ctx, string(state.Status)); err != nil {
		return err
	}
	return RegistrationSummary.Set(ctx, fmt.Sprintf("%s · %s · %s", attendeeName(state), state.Email, state.Status))
}

func validateRegistrationInput(input RegistrationInput) error {
	if strings.TrimSpace(input.RegistrationID) == "" || strings.TrimSpace(input.RegistrationToken) == "" || strings.TrimSpace(input.TicketToken) == "" {
		return fmt.Errorf("registration identity is required")
	}
	if strings.TrimSpace(input.FirstName) == "" || strings.TrimSpace(input.LastName) == "" || !strings.Contains(input.Email, "@") {
		return fmt.Errorf("attendee name and email are required")
	}
	if input.AcceptedTermsAt.IsZero() || input.CreatedAt.IsZero() {
		return fmt.Errorf("terms acceptance and creation timestamps are required")
	}
	if input.Event.PriceMinor < 1 || len(input.Event.Currency) != 3 {
		return fmt.Errorf("event price and currency are invalid")
	}
	return nil
}

func validatePaidSession(state RegistrationState, session stripe.CheckoutSession) error {
	if session.ClientReferenceID != state.RegistrationID || session.ID == "" {
		return fmt.Errorf("Stripe registration identity does not match")
	}
	if state.CheckoutSessionID != "" && session.ID != state.CheckoutSessionID {
		return fmt.Errorf("Stripe Checkout Session does not match")
	}
	if !strings.EqualFold(session.Currency, state.Event.Currency) || session.AmountTotal != state.Event.PriceMinor {
		return fmt.Errorf("Stripe amount or currency does not match")
	}
	if session.PaymentStatus != "paid" {
		return fmt.Errorf("Stripe session is not paid")
	}
	return nil
}

func mapCheckoutInput(input RegistrationInput) stripe.CreateACHCheckoutSessionInput {
	registrationURL := strings.TrimRight(input.PublicBaseURL, "/") + "/registration/" + input.RegistrationToken + "/payment"
	return stripe.CreateACHCheckoutSessionInput{
		ClientReferenceID: input.RegistrationID,
		CustomerEmail:     input.Email,
		SuccessURL:        registrationURL,
		CancelURL:         registrationURL,
		Currency:          strings.ToLower(input.Event.Currency),
		UnitAmount:        input.Event.PriceMinor,
		ProductName:       input.Event.Name + " ticket",
		Metadata:          map[string]string{"registration_id": input.RegistrationID, "event_id": input.Event.EventID},
		ExpiresAt:         input.CreatedAt.Add(30 * time.Minute),
	}
}

func mapTicketEmail(state RegistrationState) gmail.SendMessageInput {
	name := attendeeName(state)
	textBody := fmt.Sprintf("Hi %s,\n\nYour payment is confirmed. Open your private event ticket here:\n%s\n\nKeep this link private and present its QR code at check-in.", name, state.TicketURL)
	htmlBody := fmt.Sprintf("<p>Hi %s,</p><p>Your payment is confirmed.</p><p><a href=\"%s\">Open your private event ticket</a></p><p>Keep this link private and present its QR code at check-in.</p>", html.EscapeString(name), html.EscapeString(state.TicketURL))
	return gmail.SendMessageInput{To: []string{state.Email}, Subject: "Your ticket for " + state.Event.Name, TextBody: textBody, HTMLBody: htmlBody}
}

func attendeeName(state RegistrationState) string {
	return strings.TrimSpace(state.FirstName + " " + state.LastName)
}

func shortTicketCode(registrationID string) string {
	trimmed := strings.TrimPrefix(registrationID, "registration-")
	trimmed = strings.ReplaceAll(trimmed, "-", "")
	if len(trimmed) > 10 {
		trimmed = trimmed[:10]
	}
	return strings.ToUpper(trimmed)
}

var _ dex.Flow = (*RegistrationFlow)(nil)
var _ dex.Step[RegistrationInput] = InitializeRegistration{}
var _ dex.Step[stripe.CreateACHCheckoutSessionResult] = CheckoutCreated{}
var _ dex.Step[stripe.CreateACHCheckoutSessionResult] = CheckoutSetupFailed{}
var _ dex.Step[RegistrationState] = AwaitPayment{}
var _ dex.Step[PaymentEvent] = ConfirmPayment{}
var _ dex.Step[releaseInput] = ReleaseRegistration{}
var _ dex.Step[stripe.GetCheckoutSessionResult] = CheckoutReconciled{}
var _ dex.Step[stripe.GetCheckoutSessionResult] = ReconcileFailed{}
var _ dex.Step[gmail.SendMessageResult] = TicketEmailSent{}
var _ dex.Step[gmail.SendMessageResult] = TicketEmailFailed{}
var _ dex.Step[RegistrationState] = MaintainRegistration{}
var _ dex.RPC[dex.None, map[string]any] = (*RegistrationFlow)(nil).GetDexSummary
var _ dex.RPC[dex.None, map[string]any] = (*RegistrationFlow)(nil).GetDexDisplay
var _ dex.RPC[dex.None, RegistrationState] = (*RegistrationFlow)(nil).DescribeRegistration
var _ dex.RPC[PaymentEvent, PaymentEventResult] = (*RegistrationFlow)(nil).ReceiveStripeEvent
var _ dex.RPC[CheckInInput, CheckInResult] = (*RegistrationFlow)(nil).CheckIn
var _ dex.RPC[dex.None, dex.None] = (*RegistrationFlow)(nil).CancelRegistration
var _ dex.RPC[dex.None, dex.None] = (*RegistrationFlow)(nil).ResendTicket
var _ dex.RPC[dex.None, dex.None] = (*RegistrationFlow)(nil).ReconcilePayment
var _ dex.RPC[dex.None, dex.None] = (*RegistrationFlow)(nil).UndoCheckIn
