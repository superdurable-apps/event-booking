import { useCallback, useEffect, useRef, useState } from 'react';
import { checkInTicket, createRegistration, getEvent, getRegistration, getTicket } from './api/generated/sdk.gen';
import type { CheckInView, ErrorResponse, EventView, RegistrationView, TicketView } from './api/generated/types.gen';
import { MockControls } from './MockControls';

type PageLinkProps = { href: string; children: React.ReactNode; primary?: boolean };

const registrationPollIntervalMs = 2_000;

function PageLink({ href, children, primary = false }: PageLinkProps) {
  return <a className={primary ? 'button-link primary' : 'button-link'} href={href}>{children}</a>;
}

function Shell({ event, children }: { event?: EventView; children: React.ReactNode }) {
  return (
    <main>
      <header className="site-header">
        <a className="site-name" href="/">{event?.name ?? 'Event registration'}</a>
        <nav aria-label="Primary navigation">
          <a href="/register">Register</a>
          <a href="/check-in">Staff check-in</a>
        </nav>
      </header>
      {children}
      <footer>
        <p>Questions? Contact <a href={`mailto:${event?.organizerEmail ?? 'events@example.com'}`}>{event?.organizerEmail ?? 'the organizer'}</a>.</p>
      </footer>
    </main>
  );
}

function useEvent() {
  const [event, setEvent] = useState<EventView>();
  const [error, setError] = useState('');
  useEffect(() => {
    let active = true;
    void getEvent().then((result) => {
      if (!active) return;
      if (result.data) setEvent(result.data);
      else setError(errorMessage(result.error, 'Event details are temporarily unavailable.'));
    });
    return () => { active = false; };
  }, []);
  return { event, error };
}

function LoadingPanel({ text = 'Loading…' }: { text?: string }) {
  return <section className="panel" aria-live="polite">{text}</section>;
}

function ErrorNotice({ message }: { message: string }) {
  return <div className="error-notice" role="alert">{message}</div>;
}

function EventPage() {
  const { event, error } = useEvent();
  return (
    <Shell event={event}>
      <section className="hero">
        <p className="eyebrow">{event?.registrationOpen ? 'REGISTRATION OPEN' : 'REGISTRATION OPENS SOON'}</p>
        <h1>{event?.name ?? 'Event details coming soon'}</h1>
        <p className="lede">{event?.description ?? 'Loading event details…'}</p>
        {error && <ErrorNotice message={error} />}
        <div className="detail-grid" aria-label="Event details">
          <div><span>Date and time</span><strong>{event?.dateTimeDisplay ?? '—'}</strong></div>
          <div><span>Location</span><strong>{event?.location ?? '—'}</strong></div>
          <div><span>Ticket price</span><strong>{event?.priceDisplay ?? '—'}</strong></div>
          <div><span>Availability</span><strong>{event ? `${Math.max(0, event.capacity - event.reservedCount)} of ${event.capacity} remaining` : '—'}</strong></div>
        </div>
        {event?.registrationOpen
          ? <PageLink href="/register" primary>Register for the event</PageLink>
          : <button type="button" disabled>Registration is not open</button>}
        {event && <p className="fine-print">Registration deadline: {event.registrationDeadlineDisplay}</p>}
      </section>
    </Shell>
  );
}

function RegisterPage() {
  const { event, error: eventError } = useEvent();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  async function submit(formEvent: React.FormEvent<HTMLFormElement>) {
    formEvent.preventDefault();
    setBusy(true);
    setError('');
    const data = new FormData(formEvent.currentTarget);
    const result = await createRegistration({
      body: {
        firstName: String(data.get('firstName') ?? ''), lastName: String(data.get('lastName') ?? ''),
        email: String(data.get('email') ?? ''), acceptedPolicies: true,
      },
    });
    setBusy(false);
    if (!result.data) {
      setError(errorMessage(result.error, 'Unable to start registration.'));
      return;
    }
    sessionStorage.setItem(`registration:${result.data.registrationId}`, result.data.accessToken);
    window.location.assign(result.data.statusUrl);
  }

  return (
    <Shell event={event}>
      <section className="page-heading">
        <p className="eyebrow">ATTENDEE REGISTRATION</p>
        <h1>Reserve your ticket</h1>
        <p>One ticket per attendee. Your place stays reserved while the ACH payment is pending.</p>
      </section>
      <section className="panel">
        <h2>Attendee information</h2>
        {(error || eventError) && <ErrorNotice message={error || eventError} />}
        <form onSubmit={submit}>
          <div className="two-column">
            <label>First name<input name="firstName" autoComplete="given-name" required maxLength={80} /></label>
            <label>Last name<input name="lastName" autoComplete="family-name" required maxLength={80} /></label>
          </div>
          <label>Email address<input name="email" type="email" autoComplete="email" required maxLength={254} /></label>
          <label className="checkbox-row"><input name="terms" type="checkbox" required />I agree to the event policies.</label>
          <div className="summary-row"><span>One admission ticket</span><strong>{event?.priceDisplay ?? '—'}</strong></div>
          <button disabled={busy || !event?.registrationOpen} type="submit">{busy ? 'Reserving…' : 'Continue to secure ACH payment'}</button>
        </form>
        {!event?.registrationOpen && <p className="fine-print">Registration will be enabled when the event details and price are confirmed.</p>}
        <p className="fine-print">Bank account details are collected by Stripe and never pass through this site.</p>
      </section>
    </Shell>
  );
}

function RegistrationStatusPage({ registrationId }: { registrationId: string }) {
  const { event } = useEvent();
  const [registration, setRegistration] = useState<RegistrationView>();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const refreshInFlight = useRef(false);
  const hashToken = new URLSearchParams(window.location.hash.slice(1)).get('token') ?? '';
  const token = hashToken || sessionStorage.getItem(`registration:${registrationId}`) || '';

  const refresh = useCallback(async (showBusy = true) => {
    if (refreshInFlight.current) return;
    refreshInFlight.current = true;
    if (showBusy) setBusy(true);
    try {
      const result = await getRegistration({ path: { registrationId }, headers: { 'X-Registration-Token': token } });
      if (result.data) {
        setRegistration(result.data);
        setError('');
      } else {
        setError(errorMessage(result.error, 'Unable to load registration status.'));
      }
    } finally {
      refreshInFlight.current = false;
      if (showBusy) setBusy(false);
    }
  }, [registrationId, token]);

  useEffect(() => { void refresh(false); }, [refresh]);

  useEffect(() => {
    if (!registration || !shouldPollRegistration(registration)) return;
    const interval = window.setInterval(() => void refresh(false), registrationPollIntervalMs);
    return () => window.clearInterval(interval);
  }, [refresh, registration]);

  return (
    <Shell event={event}>
      <section className="page-heading">
        <p className="eyebrow">REGISTRATION STATUS</p>
        <h1>{registration ? statusHeading(registration) : 'Loading registration'}</h1>
        <p>{registration?.message ?? 'Retrieving the durable payment and ticket status…'}</p>
      </section>
      {error && <ErrorNotice message={error} />}
      {!registration ? <LoadingPanel /> : <>
        <section className="panel" data-registration-id={registration.registrationId}>
          <dl className="status-list">
            <div><dt>Registration</dt><dd>{registration.displayCode}</dd></div>
            <div><dt>Attendee</dt><dd>{registration.attendeeName}</dd></div>
            <div><dt>Email</dt><dd>{registration.emailMasked}</dd></div>
            <div><dt>Payment</dt><dd><span className={`status status-${registration.paymentStatus}`}>{label(registration.paymentStatus)}</span></dd></div>
            <div><dt>Ticket</dt><dd>{label(registration.ticketStatus)}</dd></div>
            <div><dt>Email delivery</dt><dd>{label(registration.emailDeliveryStatus)}</dd></div>
          </dl>
          <div className="actions">
            <button type="button" disabled={busy} onClick={() => void refresh(true)}>{busy ? 'Refreshing…' : 'Refresh status'}</button>
            {registration.checkoutUrl && registration.paymentStatus !== 'paid' && <PageLink href={registration.checkoutUrl} primary>Open secure ACH checkout</PageLink>}
            {registration.ticketUrl && <PageLink href={registration.ticketUrl} primary>Open electronic ticket</PageLink>}
          </div>
        </section>
        <MockControls registration={registration} onRegistrationChange={setRegistration} />
      </>}
    </Shell>
  );
}

function TicketPage({ registrationId }: { registrationId: string }) {
  const { event } = useEvent();
  const token = new URLSearchParams(window.location.search).get('token') ?? '';
  const [ticket, setTicket] = useState<TicketView>();
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    void getTicket({ path: { registrationId }, query: { token } }).then((result) => {
      if (!active) return;
      if (result.data) setTicket(result.data);
      else setError(errorMessage(result.error, 'Unable to load this ticket.'));
    });
    return () => { active = false; };
  }, [registrationId, token]);

  return (
    <Shell event={event}>
      <section className="page-heading">
        <p className="eyebrow">ELECTRONIC TICKET</p>
        <h1>{ticket?.checkedIn ? 'Checked in' : 'You are registered'}</h1>
        <p>{ticket?.checkedIn ? 'This ticket has already been admitted.' : 'Present this QR code at the event entrance.'}</p>
        {ticket && <TicketEmailDeliveryNotice status={ticket.emailDeliveryStatus} />}
      </section>
      {error && <ErrorNotice message={error} />}
      {!ticket ? (!error && <LoadingPanel />) : <section className="ticket panel">
        <img className="ticket-qr" src={ticket.qrImageUrl} alt="Ticket QR code" width="320" height="320" />
        <div>
          <h2>{ticket.eventName}</h2>
          <dl className="ticket-details">
            <div><dt>Attendee</dt><dd>{ticket.attendeeName}</dd></div>
            <div><dt>Ticket ID</dt><dd>{ticket.displayCode}</dd></div>
            <div><dt>Date</dt><dd>{ticket.dateTimeDisplay}</dd></div>
            <div><dt>Location</dt><dd>{ticket.location}</dd></div>
            <div><dt>Status</dt><dd>{ticket.checkedIn ? `Checked in${ticket.checkedInAt ? ` at ${new Date(ticket.checkedInAt).toLocaleString()}` : ''}` : 'Valid for entry'}</dd></div>
          </dl>
        </div>
      </section>}
    </Shell>
  );
}

function CheckInPage() {
  const { event } = useEvent();
  const [staffToken, setStaffToken] = useState(() => sessionStorage.getItem('staff-checkin-token') ?? '');
  const [ticketCode, setTicketCode] = useState('');
  const [result, setResult] = useState<CheckInView>();
  const [error, setError] = useState('');
  const [scanning, setScanning] = useState(false);
  const videoRef = useRef<HTMLVideoElement>(null);
	const streamRef = useRef<MediaStream | null>(null);

  useEffect(() => () => stopCamera(streamRef.current), []);

  async function submit(code = ticketCode) {
    setError('');
    sessionStorage.setItem('staff-checkin-token', staffToken);
    const response = await checkInTicket({ body: { ticketCode: code }, headers: { 'X-Staff-Token': staffToken } });
    if (response.data) setResult(response.data);
    else setError(errorMessage(response.error, 'Unable to check in this ticket.'));
  }

  async function startCamera() {
    setError('');
    const Detector = (window as unknown as { BarcodeDetector?: new (options: { formats: string[] }) => { detect(source: HTMLVideoElement): Promise<Array<{ rawValue: string }>> } }).BarcodeDetector;
    if (!Detector || !navigator.mediaDevices?.getUserMedia) {
      setError('QR camera scanning is not supported in this browser. Enter the ticket URL manually.');
      return;
    }
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' }, audio: false });
      streamRef.current = stream;
      if (!videoRef.current) return;
      videoRef.current.srcObject = stream;
      await videoRef.current.play();
      setScanning(true);
      const detector = new Detector({ formats: ['qr_code'] });
      const scan = async () => {
        if (!streamRef.current || !videoRef.current) return;
        const codes = await detector.detect(videoRef.current);
        if (codes[0]?.rawValue) {
          const code = codes[0].rawValue;
          setTicketCode(code);
          stopCamera(streamRef.current);
			streamRef.current = null;
          setScanning(false);
          await submit(code);
          return;
        }
        window.setTimeout(() => void scan(), 250);
      };
      void scan();
    } catch {
      setError('Camera access was not available. Enter the ticket URL manually.');
      setScanning(false);
    }
  }

  return (
    <Shell event={event}>
      <section className="page-heading">
        <p className="eyebrow">STAFF ONLY</p>
        <h1>Ticket check-in</h1>
        <p>Scan the QR code or paste its ticket URL. The same ticket cannot be admitted twice.</p>
      </section>
      <label className="staff-token panel">Staff access token<input type="password" value={staffToken} onChange={(e) => setStaffToken(e.target.value)} minLength={16} /></label>
      {error && <ErrorNotice message={error} />}
      <div className="check-in-grid">
        <section className="panel">
          <h2>Scan a QR ticket</h2>
          <video className="camera-preview" ref={videoRef} muted playsInline aria-label="Camera preview" />
          <button type="button" onClick={() => void startCamera()} disabled={scanning || staffToken.length < 16}>{scanning ? 'Scanning…' : 'Start camera'}</button>
        </section>
        <section className="panel">
          <h2>Enter a ticket URL</h2>
          <label>Ticket code or URL<input value={ticketCode} onChange={(e) => setTicketCode(e.target.value)} /></label>
          <button type="button" onClick={() => void submit()} disabled={ticketCode.length === 0 || staffToken.length < 16}>Check in attendee</button>
          {result && <div className={`check-in-result ${result.result}`} role="status">
            <strong>{result.result === 'checked_in' ? 'Check-in successful' : 'Already checked in'}</strong>
            <span>{result.attendeeName} · {result.displayCode}</span>
            <span>{new Date(result.checkedInAt).toLocaleString()}</span>
          </div>}
        </section>
      </div>
    </Shell>
  );
}

function NotFoundPage() {
  const { event } = useEvent();
  return <Shell event={event}><section className="page-heading"><p className="eyebrow">PAGE NOT FOUND</p><h1>This page does not exist</h1><PageLink href="/">Return to event</PageLink></section></Shell>;
}

function errorMessage(error: unknown, fallback: string) {
  if (error && typeof error === 'object' && 'message' in error) return String((error as ErrorResponse).message);
  return fallback;
}

function label(value: string) { return value.replaceAll('_', ' '); }

function statusHeading(registration: RegistrationView) {
  if (registration.state === 'ticket_ready' || registration.state === 'ticket_email_review_required') return 'Your ticket is ready';
  if (registration.state === 'checked_in') return 'You are checked in';
  if (registration.state === 'capacity_full') return 'Event capacity reached';
  if (registration.paymentStatus === 'failed') return 'Payment failed';
  if (registration.paymentStatus === 'expired') return 'Checkout expired';
  if (registration.paymentStatus === 'paid') return 'Payment received';
  return 'Payment is processing';
}

function TicketEmailDeliveryNotice({ status }: { status: TicketView['emailDeliveryStatus'] }) {
  const message = status === 'sent'
    ? 'A copy was sent by email.'
    : status === 'failed'
      ? 'Your ticket is valid online, but email delivery failed. Please contact the organizer.'
      : status === 'unknown'
        ? 'Your ticket is valid online, but email delivery could not be confirmed. Please contact the organizer.'
        : 'Your ticket is valid online. Email delivery is still processing.';
  const needsAttention = status === 'failed' || status === 'unknown';
  return <p className={needsAttention ? 'error-notice' : 'fine-print'} role="status">{message}</p>;
}

function shouldPollRegistration(registration: RegistrationView) {
  return registration.state === 'reserving'
    || registration.state === 'checkout_creating'
    || registration.state === 'payment_pending'
    || registration.state === 'paid'
    || registration.state === 'ticket_sending';
}

function stopCamera(stream?: MediaStream | null) { stream?.getTracks().forEach((track) => track.stop()); }

export function App() {
  const path = window.location.pathname.replace(/\/$/, '') || '/';
  const registrationMatch = path.match(/^\/registration\/([^/]+)$/);
  const ticketMatch = path.match(/^\/ticket\/([^/]+)$/);
  if (path === '/') return <EventPage />;
  if (path === '/register') return <RegisterPage />;
  if (path === '/check-in') return <CheckInPage />;
  if (registrationMatch) return <RegistrationStatusPage registrationId={registrationMatch[1]} />;
  if (ticketMatch) return <TicketPage registrationId={ticketMatch[1]} />;
  return <NotFoundPage />;
}
