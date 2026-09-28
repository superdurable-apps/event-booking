package process

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	gmail "github.com/superdurable/dex-connectors-library/connectors/google/gmail"
	"github.com/superdurable/dex-connectors-library/connectors/stripe"
	"github.com/superdurable/dex-connectors-library/sdkgo"
	"github.com/superdurable/dex/sdk-go/dex"
)

const (
	StripeConnectionName = "stripe-payments"
	StripeTriggerBinding = "registration-checkout-updates"
	GmailConnectionName  = "gmail-tickets"
	checkoutStepType     = "CreateRegistrationACHCheckout"
	ticketEmailStepType  = "SendRegistrationTicketEmail"
)

type RegistrationState string

const (
	StateReserving                 RegistrationState = "reserving"
	StateCapacityFull              RegistrationState = "capacity_full"
	StateCheckoutCreating          RegistrationState = "checkout_creating"
	StatePaymentPending            RegistrationState = "payment_pending"
	StatePaymentFailed             RegistrationState = "payment_failed"
	StatePaymentExpired            RegistrationState = "payment_expired"
	StatePaymentReviewRequired     RegistrationState = "payment_review_required"
	StatePaid                      RegistrationState = "paid"
	StateTicketSending             RegistrationState = "ticket_sending"
	StateTicketReady               RegistrationState = "ticket_ready"
	StateTicketEmailReviewRequired RegistrationState = "ticket_email_review_required"
	StateCheckedIn                 RegistrationState = "checked_in"
)

type PaymentStatus string

const (
	PaymentNotStarted     PaymentStatus = "not_started"
	PaymentPending        PaymentStatus = "pending"
	PaymentFailed         PaymentStatus = "failed"
	PaymentExpired        PaymentStatus = "expired"
	PaymentReviewRequired PaymentStatus = "review_required"
	PaymentPaid           PaymentStatus = "paid"
)

type TicketStatus string

const (
	TicketNotAvailable        TicketStatus = "not_available"
	TicketSending             TicketStatus = "sending"
	TicketReady               TicketStatus = "ready"
	TicketEmailReviewRequired TicketStatus = "email_review_required"
	TicketCheckedIn           TicketStatus = "checked_in"
)

type EmailDeliveryStatus string

const (
	EmailDeliveryNotStarted EmailDeliveryStatus = "not_started"
	EmailDeliverySending    EmailDeliveryStatus = "sending"
	EmailDeliverySent       EmailDeliveryStatus = "sent"
	EmailDeliveryFailed     EmailDeliveryStatus = "failed"
	EmailDeliveryUnknown    EmailDeliveryStatus = "unknown"
)

type RegistrationInput struct {
	RegistrationID string `json:"registrationId"`
	FirstName      string `json:"firstName"`
	LastName       string `json:"lastName"`
	Email          string `json:"email"`
}

type RegistrationRecord struct {
	RegistrationID          string              `json:"registrationId"`
	FirstName               string              `json:"firstName"`
	LastName                string              `json:"lastName"`
	Email                   string              `json:"email"`
	State                   RegistrationState   `json:"state"`
	PaymentStatus           PaymentStatus       `json:"paymentStatus"`
	TicketStatus            TicketStatus        `json:"ticketStatus"`
	EmailDeliveryStatus     EmailDeliveryStatus `json:"emailDeliveryStatus"`
	StripeSessionID         string              `json:"stripeSessionId,omitempty"`
	CheckoutURL             string              `json:"checkoutUrl,omitempty"`
	CheckedInAt             *time.Time          `json:"checkedInAt,omitempty"`
	ProcessedPaymentEventID []string            `json:"processedPaymentEventIds,omitempty"`
}

type RegistrationResult struct {
	Record RegistrationRecord `json:"record"`
}

type ApplyStripeEventInput struct {
	EventID    string                      `json:"eventId"`
	OccurredAt time.Time                   `json:"occurredAt"`
	Event      stripe.CheckoutSessionEvent `json:"event"`
}

type ApplyStripeEventResult struct {
	Accepted  bool              `json:"accepted"`
	Duplicate bool              `json:"duplicate"`
	State     RegistrationState `json:"state"`
}

type EmailPayload struct {
	RegistrationID string `json:"registrationId"`
	FirstName      string `json:"firstName"`
	LastName       string `json:"lastName"`
	Email          string `json:"email"`
}

var (
	RegistrationData = dex.DefineAttribute[RegistrationRecord]("registration-data")
	// dex:indexed-attribute attribute-key:registration-state index-key:registration-state index-type:keyword value-type:string description:"Current registration state"
	RegistrationStateAttribute = dex.DefineAttribute[string](
		"registration-state", dex.Indexed(dex.AttributeIndex{Type: dex.IndexKeyword}),
	)
	StripeCheckoutResult = dex.DefineAttribute[stripe.CreateACHCheckoutSessionResult]("stripe-checkout-result")
	TicketEmailResult    = dex.DefineAttribute[gmail.SendMessageResult]("ticket-email-result")
	PaymentEvents        = dex.DefineChannel[ApplyStripeEventInput]("registration-payment-events")
	CheckInEvents        = dex.DefineChannel[bool]("registration-check-in-events")
	ResendTicketEvents   = dex.DefineChannel[bool]("registration-resend-ticket-events")
)

type RegistrationFlow struct {
	dex.FlowDefaults
	config   EventConfig
	capacity *CapacityClient
	stripe   stripe.Connection
	gmail    gmail.Connection
	tokens   *TokenSigner
}

func NewRegistrationFlow(config EventConfig, capacity *CapacityClient, stripeConnection stripe.Connection, gmailConnection gmail.Connection, tokens *TokenSigner) *RegistrationFlow {
	return &RegistrationFlow{config: config, capacity: capacity, stripe: stripeConnection, gmail: gmailConnection, tokens: tokens}
}

// dex:group group-id:registration group-label:"Registration"
// dex:explanation text:"Validate and persist the attendee registration before reserving capacity."
type InitializeRegistration struct {
	dex.StepDefaultsNoWaitFor[RegistrationInput]
}

func (InitializeRegistration) Execute(ctx dex.Context, input RegistrationInput) (*dex.StepDecision, error) {
	record := RegistrationRecord{
		RegistrationID: input.RegistrationID,
		FirstName:      strings.TrimSpace(input.FirstName), LastName: strings.TrimSpace(input.LastName),
		Email: strings.ToLower(strings.TrimSpace(input.Email)), State: StateReserving,
		PaymentStatus: PaymentNotStarted, TicketStatus: TicketNotAvailable, EmailDeliveryStatus: EmailDeliveryNotStarted,
	}
	if record.RegistrationID == "" || record.FirstName == "" || record.LastName == "" || record.Email == "" {
		return nil, fmt.Errorf("registration identity, name, and email are required")
	}
	if err := setRegistration(ctx, record); err != nil {
		return nil, err
	}
	return dex.GoTo(ReserveRegistrationSeat{}, input), nil
}

// dex:group group-id:registration group-label:"Registration"
// dex:explanation text:"Atomically reserve one place in the shared event capacity Flow."
type ReserveRegistrationSeat struct {
	dex.StepDefaultsNoWaitFor[RegistrationInput]
	capacity *CapacityClient
}

func (step ReserveRegistrationSeat) Execute(ctx dex.Context, input RegistrationInput) (*dex.StepDecision, error) {
	reservation, err := step.capacity.Reserve(ctx, input.RegistrationID)
	if err != nil {
		return nil, err
	}
	record, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !reservation.Accepted {
		record.State = StateCapacityFull
		if err := setRegistration(ctx, record); err != nil {
			return nil, err
		}
		return dex.GracefulComplete(RegistrationResult{Record: record}), nil
	}
	record.State = StateCheckoutCreating
	if err := setRegistration(ctx, record); err != nil {
		return nil, err
	}
	return dex.GoTo(sdkgo.StepRef[RegistrationInput](checkoutStepType), input), nil
}

// dex:group group-id:payment group-label:"Payment"
// dex:explanation text:"Store the Stripe Checkout Session and wait durably for ACH settlement."
type CheckoutCreated struct {
	dex.StepDefaultsNoWaitFor[stripe.CreateACHCheckoutSessionResult]
}

func (CheckoutCreated) Execute(ctx dex.Context, result stripe.CreateACHCheckoutSessionResult) (*dex.StepDecision, error) {
	record, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	record.StripeSessionID = result.Value.ID
	record.CheckoutURL = result.Value.URL
	record.State = StatePaymentPending
	record.PaymentStatus = PaymentPending
	if err := setRegistration(ctx, record); err != nil {
		return nil, err
	}
	return dex.GoTo(WaitForPayment{}, nil), nil
}

// dex:group group-id:payment group-label:"Payment"
// dex:explanation text:"Preserve the reservation for organizer review when Checkout creation is uncertain or rejected."
type CheckoutNeedsReview struct {
	dex.StepDefaultsNoWaitFor[stripe.CreateACHCheckoutSessionResult]
}

func (CheckoutNeedsReview) Execute(ctx dex.Context, result stripe.CreateACHCheckoutSessionResult) (*dex.StepDecision, error) {
	record, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	if result.Value.ID != "" {
		record.StripeSessionID = result.Value.ID
	}
	if result.Value.URL != "" {
		record.CheckoutURL = result.Value.URL
	}
	record.State = StatePaymentReviewRequired
	record.PaymentStatus = PaymentReviewRequired
	if err := setRegistration(ctx, record); err != nil {
		return nil, err
	}
	return dex.GoTo(WaitForPayment{}, nil), nil
}

// dex:group group-id:payment group-label:"Payment"
// dex:explanation text:"Wait durably for a verified Stripe Checkout Session webhook event."
type WaitForPayment struct{ dex.StepDefaults }

func (WaitForPayment) WaitFor(dex.Context, dex.None) (*dex.Wait, error) {
	return dex.AnyOf(PaymentEvents.ForOne()), nil
}

func (WaitForPayment) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	events, err := PaymentEvents.GetConditionResults(ctx)
	if err != nil {
		return nil, err
	}
	if len(events) != 1 {
		return nil, fmt.Errorf("payment wait completed without one event")
	}
	record, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	event := events[0]
	if event.Event.Session.ID != "" {
		record.StripeSessionID = event.Event.Session.ID
	}
	switch event.Event.Type {
	case "checkout.session.async_payment_succeeded":
		payload, err := prepareTicketDelivery(ctx, record)
		if err != nil {
			return nil, err
		}
		return dex.GoTo(sdkgo.StepRef[EmailPayload](ticketEmailStepType), payload), nil
	case "checkout.session.completed":
		if event.Event.Session.PaymentStatus == "paid" {
			payload, err := prepareTicketDelivery(ctx, record)
			if err != nil {
				return nil, err
			}
			return dex.GoTo(sdkgo.StepRef[EmailPayload](ticketEmailStepType), payload), nil
		}
		record.State = StatePaymentPending
		record.PaymentStatus = PaymentPending
		if err := setRegistration(ctx, record); err != nil {
			return nil, err
		}
		return dex.GoTo(WaitForPayment{}, nil), nil
	case "checkout.session.async_payment_failed":
		record.State = StatePaymentFailed
		record.PaymentStatus = PaymentFailed
		if err := setRegistration(ctx, record); err != nil {
			return nil, err
		}
		return dex.GoTo(ReleaseRegistrationSeat{}, nil), nil
	case "checkout.session.expired":
		record.State = StatePaymentExpired
		record.PaymentStatus = PaymentExpired
		if err := setRegistration(ctx, record); err != nil {
			return nil, err
		}
		return dex.GoTo(ReleaseRegistrationSeat{}, nil), nil
	default:
		return dex.GoTo(WaitForPayment{}, nil), nil
	}
}

func prepareTicketDelivery(ctx dex.Context, record RegistrationRecord) (EmailPayload, error) {
	markTicketEmailSending(&record)
	if err := setRegistration(ctx, record); err != nil {
		return EmailPayload{}, err
	}
	return EmailPayload{RegistrationID: record.RegistrationID, FirstName: record.FirstName, LastName: record.LastName, Email: record.Email}, nil
}

// dex:group group-id:registration group-label:"Registration"
// dex:explanation text:"Release the reserved place after payment fails or the Checkout Session expires."
type ReleaseRegistrationSeat struct {
	dex.StepDefaultsNoWaitFor[dex.None]
	capacity *CapacityClient
}

func (step ReleaseRegistrationSeat) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	record, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := step.capacity.Release(ctx, record.RegistrationID); err != nil {
		return nil, err
	}
	return dex.GracefulComplete(RegistrationResult{Record: record}), nil
}

// dex:group group-id:ticket group-label:"Ticket"
// dex:explanation text:"Mark the electronic ticket ready after Gmail accepts the ticket email."
type TicketEmailSent struct {
	dex.StepDefaultsNoWaitFor[gmail.SendMessageResult]
}

func (TicketEmailSent) Execute(ctx dex.Context, _ gmail.SendMessageResult) (*dex.StepDecision, error) {
	record, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	markTicketEmailSent(&record)
	if err := setRegistration(ctx, record); err != nil {
		return nil, err
	}
	return dex.GoTo(WaitForAttendance{}, nil), nil
}

// dex:group group-id:ticket group-label:"Ticket"
// dex:explanation text:"Keep the paid ticket available online when email delivery needs organizer review."
type TicketEmailNeedsReview struct {
	dex.StepDefaultsNoWaitFor[gmail.SendMessageResult]
}

func (TicketEmailNeedsReview) Execute(ctx dex.Context, result gmail.SendMessageResult) (*dex.StepDecision, error) {
	record, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	markTicketEmailNeedsReview(&record, result)
	if err := setRegistration(ctx, record); err != nil {
		return nil, err
	}
	return dex.GoTo(WaitForAttendance{}, nil), nil
}

// dex:group group-id:attendance group-label:"Attendance"
// dex:explanation text:"Wait durably for ticket check-in or an organizer-requested email resend."
type WaitForAttendance struct{ dex.StepDefaults }

func (WaitForAttendance) WaitFor(dex.Context, dex.None) (*dex.Wait, error) {
	return dex.AnyOf(CheckInEvents.ForOne(), ResendTicketEvents.ForOne()), nil
}

func (WaitForAttendance) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	checkIns, err := CheckInEvents.GetConditionResults(ctx)
	if err != nil {
		return nil, err
	}
	if len(checkIns) == 1 && checkIns[0] {
		record, err := RegistrationData.Get(ctx)
		if err != nil {
			return nil, err
		}
		return dex.GracefulComplete(RegistrationResult{Record: record}), nil
	}
	resends, err := ResendTicketEvents.GetConditionResults(ctx)
	if err != nil {
		return nil, err
	}
	if len(resends) == 1 && resends[0] {
		record, err := RegistrationData.Get(ctx)
		if err != nil {
			return nil, err
		}
		markTicketEmailSending(&record)
		if err := setRegistration(ctx, record); err != nil {
			return nil, err
		}
		return dex.GoTo(sdkgo.StepRef[EmailPayload](ticketEmailStepType), EmailPayload{
			RegistrationID: record.RegistrationID, FirstName: record.FirstName, LastName: record.LastName, Email: record.Email,
		}), nil
	}
	return nil, fmt.Errorf("attendance wait completed without check-in or resend")
}

func (flow *RegistrationFlow) GetSteps() []dex.StepDef {
	return []dex.StepDef{
		dex.DefineStartStep(InitializeRegistration{}), dex.DefineStep(ReserveRegistrationSeat{capacity: flow.capacity}),
		dex.DefineStep(stripe.NewCreateACHCheckoutSessionStep(stripe.CreateACHCheckoutSessionStepConfig[RegistrationInput]{
			StepType: checkoutStepType, ConnectionName: StripeConnectionName, Connection: flow.stripe,
			Annotations: sdkgo.StepAnnotations{GroupID: "payment", GroupLabel: "Payment", Explanation: "Create a Stripe-hosted Checkout Session that accepts ACH."},
			MapToOperationInput: func(input RegistrationInput) stripe.CreateACHCheckoutSessionInput {
				statusURL := flow.tokens.StatusURL(input.RegistrationID)
				return stripe.CreateACHCheckoutSessionInput{
					ClientReferenceID: input.RegistrationID, CustomerEmail: input.Email,
					SuccessURL: statusURL, CancelURL: statusURL, Currency: flow.config.Currency,
					UnitAmount: flow.config.PriceCents, ProductName: flow.config.Name,
					Metadata: map[string]string{"registration_id": input.RegistrationID, "event_id": flow.config.ID},
				}
			},
			Created: sdkgo.GoTo(CheckoutCreated{}), ProviderRejected: sdkgo.GoTo(CheckoutNeedsReview{}),
			Uncertain: sdkgo.GoTo(CheckoutNeedsReview{}), InvalidResponse: sdkgo.GoTo(CheckoutNeedsReview{}), Defect: sdkgo.GoTo(CheckoutNeedsReview{}),
			ResultAttribute: &StripeCheckoutResult,
		})), dex.DefineStep(CheckoutCreated{}), dex.DefineStep(CheckoutNeedsReview{}),
		dex.DefineStep(WaitForPayment{}), dex.DefineStep(ReleaseRegistrationSeat{capacity: flow.capacity}),
		dex.DefineStep(gmail.NewSendMessageStep(gmail.SendMessageStepConfig[EmailPayload]{
			StepType: ticketEmailStepType, ConnectionName: GmailConnectionName, Connection: flow.gmail,
			Annotations:         sdkgo.StepAnnotations{GroupID: "ticket", GroupLabel: "Ticket", Explanation: "Email the paid attendee an electronic ticket and QR code."},
			MapToOperationInput: func(payload EmailPayload) gmail.SendMessageInput { return flow.ticketEmail(payload) },
			Sent:                sdkgo.GoTo(TicketEmailSent{}), ProviderRejected: sdkgo.GoTo(TicketEmailNeedsReview{}),
			Uncertain: sdkgo.GoTo(TicketEmailNeedsReview{}), Defect: sdkgo.GoTo(TicketEmailNeedsReview{}),
			ResultAttribute: &TicketEmailResult,
		})), dex.DefineStep(TicketEmailSent{}), dex.DefineStep(TicketEmailNeedsReview{}), dex.DefineStep(WaitForAttendance{}),
	}
}

func (flow *RegistrationFlow) GetRPCs() []dex.RPCDef {
	locks := []dex.AttributeLock{dex.LockAttribute(RegistrationData), dex.LockAttribute(RegistrationStateAttribute)}
	return []dex.RPCDef{
		dex.DefineRPC(flow.GetDexSummary, nil), dex.DefineRPC(flow.GetDexDisplay, nil),
		dex.DefineRPC(flow.DescribeRegistration, &dex.RPCOptions{}),
		dex.DefineRPC(flow.ApplyStripeEvent, &dex.RPCOptions{IsTransactional: true, LockAttributes: locks}),
		dex.DefineRPC(flow.CheckIn, &dex.RPCOptions{IsTransactional: true, LockAttributes: locks, Action: dex.DefineAction(
			"Check in", dex.WhenAttributeMatches(RegistrationStateAttribute,
				dex.AttributeMatchEqual(string(StateTicketReady)), dex.AttributeMatchEqual(string(StateTicketEmailReviewRequired)), dex.AttributeMatchEqual(string(StateCheckedIn))),
			dex.ActionRequiresPermission("checkin.manage"),
		)}),
		dex.DefineRPC(flow.ResendTicket, &dex.RPCOptions{IsTransactional: true, LockAttributes: locks, Action: dex.DefineAction(
			"Resend ticket", dex.WhenAttributeMatches(RegistrationStateAttribute,
				dex.AttributeMatchEqual(string(StateTicketReady)), dex.AttributeMatchEqual(string(StateTicketEmailReviewRequired))),
			dex.ActionRequiresPermission("registration.resend-ticket"),
		)}),
	}
}

func (*RegistrationFlow) GetPersistenceSchema() dex.PersistenceSchema {
	return dex.PersistenceSchema{
		Attributes: []dex.AttributeDef{RegistrationData, RegistrationStateAttribute, StripeCheckoutResult, TicketEmailResult},
		Channels:   []dex.ChannelDef{PaymentEvents, CheckInEvents, ResendTicketEvents},
	}
}

func (*RegistrationFlow) GetConnectorTriggerBindings() []sdkgo.TriggerBindingDefinition {
	return []sdkgo.TriggerBindingDefinition{stripe.DefineCheckoutSessionUpdatedTriggerBinding(stripe.CheckoutSessionUpdatedTriggerBindingConfig{
		ConnectionName: StripeConnectionName, BindingName: StripeTriggerBinding,
	})}
}

func (*RegistrationFlow) DescribeRegistration(ctx dex.Context, _ dex.None) (*dex.RPCResult[RegistrationRecord], error) {
	record, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &dex.RPCResult[RegistrationRecord]{Output: record}, nil
}

func (*RegistrationFlow) ApplyStripeEvent(ctx dex.Context, input ApplyStripeEventInput) (*dex.RPCResult[ApplyStripeEventResult], error) {
	record, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	if input.EventID == "" || input.Event.Session.ClientReferenceID != record.RegistrationID {
		return nil, sdkgo.MarkTriggerUndeliverable(fmt.Errorf("Stripe event registration reference does not match Flow"))
	}
	for _, processed := range record.ProcessedPaymentEventID {
		if processed == input.EventID {
			return &dex.RPCResult[ApplyStripeEventResult]{Output: ApplyStripeEventResult{Duplicate: true, State: record.State}}, nil
		}
	}
	record.ProcessedPaymentEventID = append(record.ProcessedPaymentEventID, input.EventID)
	if len(record.ProcessedPaymentEventID) > 100 {
		record.ProcessedPaymentEventID = record.ProcessedPaymentEventID[len(record.ProcessedPaymentEventID)-100:]
	}
	if input.Event.Session.ID != "" {
		record.StripeSessionID = input.Event.Session.ID
	}
	if err := setRegistration(ctx, record); err != nil {
		return nil, err
	}
	if err := PaymentEvents.Publish(ctx, input); err != nil {
		return nil, err
	}
	return &dex.RPCResult[ApplyStripeEventResult]{Output: ApplyStripeEventResult{Accepted: true, State: record.State}}, nil
}

func (*RegistrationFlow) CheckIn(ctx dex.Context, _ dex.None) (*dex.RPCResult[dex.None], error) {
	record, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	if record.CheckedInAt != nil || record.State == StateCheckedIn {
		return &dex.RPCResult[dex.None]{}, nil
	}
	if !ticketAvailable(record) {
		return nil, fmt.Errorf("ticket is not available for check-in")
	}
	checkedInAt := ctx.FirstAttemptAt().UTC()
	record.CheckedInAt = &checkedInAt
	record.State = StateCheckedIn
	record.TicketStatus = TicketCheckedIn
	if err := setRegistration(ctx, record); err != nil {
		return nil, err
	}
	if err := CheckInEvents.Publish(ctx, true); err != nil {
		return nil, err
	}
	return &dex.RPCResult[dex.None]{}, nil
}

func (*RegistrationFlow) ResendTicket(ctx dex.Context, _ dex.None) (*dex.RPCResult[dex.None], error) {
	record, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !ticketAvailable(record) || record.State == StateCheckedIn {
		return &dex.RPCResult[dex.None]{}, nil
	}
	if err := ResendTicketEvents.Publish(ctx, true); err != nil {
		return nil, err
	}
	return &dex.RPCResult[dex.None]{}, nil
}

func (flow *RegistrationFlow) GetDexSummary(ctx dex.Context, _ dex.None) (*dex.RPCResult[map[string]any], error) {
	state, err := RegistrationStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	_ = state
	return &dex.RPCResult[map[string]any]{Output: map[string]any{}}, nil
}

// dex:field attribute-key:registration-data value-type:json editable:false description:"Registration, payment, ticket, and check-in details"
// dex:field attribute-key:registration-state value-type:string editable:false description:"Current registration state"
// dex:field attribute-key:stripe-checkout-result value-type:object editable:false description:"Stripe Checkout provider result"
// dex:field attribute-key:ticket-email-result value-type:object editable:false description:"Gmail ticket delivery provider result"
func (flow *RegistrationFlow) GetDexDisplay(ctx dex.Context, _ dex.None) (*dex.RPCResult[map[string]any], error) {
	record, err := RegistrationData.Get(ctx)
	if err != nil {
		return nil, err
	}
	state, err := RegistrationStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	stripeResult, err := StripeCheckoutResult.Get(ctx)
	if err != nil && !isMissingAttribute(err) {
		return nil, err
	}
	emailResult, err := TicketEmailResult.Get(ctx)
	if err != nil && !isMissingAttribute(err) {
		return nil, err
	}
	return &dex.RPCResult[map[string]any]{Output: map[string]any{
		"registration-data": record, "registration-state": state,
		"stripe-checkout-result": stripeResult, "ticket-email-result": emailResult,
	}}, nil
}

func isMissingAttribute(err error) bool {
	var missing *dex.AttributeNotFoundError
	return errors.As(err, &missing)
}

func setRegistration(ctx dex.Context, record RegistrationRecord) error {
	if err := RegistrationData.Set(ctx, record); err != nil {
		return err
	}
	return RegistrationStateAttribute.Set(ctx, string(record.State))
}

func ticketAvailable(record RegistrationRecord) bool {
	return record.PaymentStatus == PaymentPaid && (record.TicketStatus == TicketSending || record.TicketStatus == TicketReady || record.TicketStatus == TicketEmailReviewRequired || record.TicketStatus == TicketCheckedIn)
}

func markTicketEmailSending(record *RegistrationRecord) {
	record.State = StateTicketSending
	record.PaymentStatus = PaymentPaid
	record.TicketStatus = TicketSending
	record.EmailDeliveryStatus = EmailDeliverySending
}

func markTicketEmailSent(record *RegistrationRecord) {
	record.State = StateTicketReady
	record.PaymentStatus = PaymentPaid
	record.TicketStatus = TicketReady
	record.EmailDeliveryStatus = EmailDeliverySent
}

func markTicketEmailNeedsReview(record *RegistrationRecord, result gmail.SendMessageResult) {
	record.State = StateTicketEmailReviewRequired
	record.PaymentStatus = PaymentPaid
	record.TicketStatus = TicketEmailReviewRequired
	record.EmailDeliveryStatus = EmailDeliveryFailed
	if result.Branch == gmail.SendMessageBranchUncertain {
		record.EmailDeliveryStatus = EmailDeliveryUnknown
	}
}

func (flow *RegistrationFlow) ticketEmail(payload EmailPayload) gmail.SendMessageInput {
	ticketURL := flow.tokens.TicketURL(payload.RegistrationID)
	qrURL := flow.tokens.QRURL(payload.RegistrationID)
	name := strings.TrimSpace(payload.FirstName + " " + payload.LastName)
	return gmail.SendMessageInput{
		To: []string{payload.Email}, Subject: "Your ticket for " + flow.config.Name,
		TextBody: fmt.Sprintf("Hi %s,\n\nYour payment has settled. Open your electronic ticket here:\n%s\n\nShow its QR code at check-in.\n", name, ticketURL),
		HTMLBody: fmt.Sprintf(`<p>Hi %s,</p><p>Your payment has settled. Your electronic ticket is ready.</p><p><a href="%s">Open your ticket</a></p><p><img src="%s" width="240" height="240" alt="Ticket QR code"></p><p>Show this QR code at check-in.</p>`,
			html.EscapeString(name), html.EscapeString(ticketURL), html.EscapeString(qrURL)),
	}
}

var _ dex.Flow = (*RegistrationFlow)(nil)
var _ dex.Step[RegistrationInput] = InitializeRegistration{}
var _ dex.Step[RegistrationInput] = ReserveRegistrationSeat{}
var _ dex.Step[stripe.CreateACHCheckoutSessionResult] = CheckoutCreated{}
var _ dex.Step[stripe.CreateACHCheckoutSessionResult] = CheckoutNeedsReview{}
var _ dex.Step[dex.None] = WaitForPayment{}
var _ dex.Step[dex.None] = ReleaseRegistrationSeat{}
var _ dex.Step[gmail.SendMessageResult] = TicketEmailSent{}
var _ dex.Step[gmail.SendMessageResult] = TicketEmailNeedsReview{}
var _ dex.Step[dex.None] = WaitForAttendance{}
