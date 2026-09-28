import { useCallback, useEffect, useRef, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { checkInTicket, createRegistration, getEvent, getRegistration, getTicket } from './api/generated/sdk.gen';
import type { CheckInView, EventView, RegistrationStatus, RegistrationView, TicketView } from './api/generated/types.gen';

function SiteHeader() {
  return (
    <header className="site-header">
      <a className="site-name" href="/">Event registration</a>
      <nav aria-label="Primary navigation">
        <a href="/">Event</a>
        <a href="/register">Register</a>
        <a href="/check-in">Staff check-in</a>
      </nav>
    </header>
  );
}

function PageShell({ children }: { children: ReactNode }) {
  return (
    <div className="site-shell">
      <SiteHeader />
      <main>{children}</main>
      <footer>
        <span>Event details are configurable until the program is confirmed.</span>
        <span>Payments are completed securely with Stripe ACH.</span>
      </footer>
    </div>
  );
}

function Field({ label, name, type = 'text', placeholder, autoComplete, required = true }: {
  label: string; name: string; type?: string; placeholder?: string; autoComplete?: string; required?: boolean;
}) {
  return (
    <label className="field" htmlFor={name}>
      <span>{label}</span>
      <input id={name} name={name} type={type} placeholder={placeholder} autoComplete={autoComplete} required={required} />
    </label>
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

function EventPage() {
  const { event, error } = useEvent();
  return (
    <PageShell>
      <section className="hero">
        <p className="eyebrow">{event?.registrationOpen ? 'REGISTRATION OPEN' : 'EVENT REGISTRATION'}</p>
        <h1>{event?.name ?? 'Event name to be announced'}</h1>
        <p className="lede">{event?.description ?? 'A short event description will appear here once the program is confirmed.'}</p>
        <div className="primary-actions">
          <a className={`button${event && !event.registrationOpen ? ' disabled' : ''}`} href="/register">Register for the event</a>
        </div>
        {error && <p className="notice error" role="alert">{error}</p>}
      </section>

      <section className="two-column" aria-label="Event overview">
        <div className="panel">
          <h2>Event details</h2>
          <dl className="details-list">
            <div><dt>Date and time</dt><dd>{event ? formatDate(event.startsAt, event.timezone) : 'To be announced'}</dd></div>
            <div><dt>Location</dt><dd>{event?.location ?? 'To be announced'}</dd></div>
            <div><dt>Ticket price</dt><dd>{event ? `${formatMoney(event.priceMinor, event.currency)} · US bank account` : 'To be announced · paid by US bank account'}</dd></div>
          </dl>
        </div>
        <div className="panel">
          <h2>Availability</h2>
          <div className="availability-number">{event ? event.remaining : '—'}</div>
          <p>{event ? `${event.remaining} of ${event.capacity} places remain.` : 'Live capacity will appear here.'}</p>
        </div>
      </section>

      <section className="panel steps-panel">
        <h2>How registration works</h2>
        <ol className="steps">
          <li><span>1</span><strong>Enter attendee details</strong><small>Name and email address</small></li>
          <li><span>2</span><strong>Pay securely</strong><small>Stripe-hosted ACH checkout</small></li>
          <li><span>3</span><strong>Receive your ticket</strong><small>Email with a private QR ticket link</small></li>
        </ol>
      </section>
    </PageShell>
  );
}

function RegistrationPage() {
  const { event } = useEvent();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  async function submit(formEvent: FormEvent<HTMLFormElement>) {
    formEvent.preventDefault();
    setSubmitting(true);
    setError('');
    const form = new FormData(formEvent.currentTarget);
    const result = await createRegistration({ body: {
      requestId: crypto.randomUUID(),
      firstName: String(form.get('firstName') ?? ''),
      lastName: String(form.get('lastName') ?? ''),
      email: String(form.get('email') ?? ''),
      acceptedTerms: true,
    } });
    if (result.data) {
      window.location.assign(`/registration/${encodeURIComponent(result.data.registrationToken)}/payment`);
      return;
    }
    setError(errorMessage(result.error, 'Unable to start registration. Please try again.'));
    setSubmitting(false);
  }

  return (
    <PageShell>
      <div className="page-heading">
        <p className="eyebrow">ATTENDEE REGISTRATION</p>
        <h1>Register for the event</h1>
        <p>One attendee and one ticket per registration.</p>
      </div>
      <form className="panel form-panel" onSubmit={submit}>
        <div className="form-grid">
          <Field label="First name" name="firstName" autoComplete="given-name" />
          <Field label="Last name" name="lastName" autoComplete="family-name" />
          <Field label="Email address" name="email" type="email" placeholder="name@example.com" autoComplete="email" />
        </div>
        <label className="checkbox-row">
          <input name="acceptedTerms" type="checkbox" required />
          <span>I agree to the event terms, payment policy, and privacy notice.</span>
        </label>
        <div className="form-summary">
          <div><span>Ticket quantity</span><strong>1</strong></div>
          <div><span>Total</span><strong>{event ? formatMoney(event.priceMinor, event.currency) : 'Loading…'}</strong></div>
        </div>
        <button type="submit" disabled={submitting || event?.registrationOpen === false}>{submitting ? 'Preparing checkout…' : 'Continue to payment'}</button>
        {error && <p className="notice error" role="alert">{error}</p>}
        <p className="form-note">Payment is completed on Stripe using a US bank account. The ticket is issued after ACH settlement succeeds.</p>
      </form>
      <a className="back-link" href="/">Back to event details</a>
    </PageShell>
  );
}

function PaymentPage({ token }: { token: string }) {
  const [registration, setRegistration] = useState<RegistrationView>();
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    let timeout = 0;
    const load = async () => {
      const result = await getRegistration({ path: { registrationToken: token } });
      if (!active) return;
      if (result.data) {
        setRegistration(result.data);
        setError('');
        if (!isTerminalRegistration(result.data.status)) timeout = window.setTimeout(load, 2000);
      } else {
        setError(errorMessage(result.error, 'Registration status is temporarily unavailable.'));
      }
    };
    void load();
    return () => { active = false; window.clearTimeout(timeout); };
  }, [token]);

  return (
    <PageShell>
      <div className="page-heading">
        <p className="eyebrow">REGISTRATION STATUS</p>
        <h1>Complete your ACH payment</h1>
        <p>{registration?.message ?? 'Preparing your durable registration and secure checkout.'}</p>
      </div>
      <section className="panel payment-layout">
        <div>
          <h2>Order summary</h2>
          <dl className="details-list">
            <div><dt>Attendee</dt><dd>{registration ? `${registration.firstName} ${registration.lastName}` : 'Loading…'}</dd></div>
            <div><dt>Ticket</dt><dd>General admission · quantity 1</dd></div>
            <div><dt>Total</dt><dd>{registration ? formatMoney(registration.amountMinor, registration.currency) : 'Loading…'}</dd></div>
            <div><dt>Status</dt><dd>{registration ? statusLabel(registration.status) : 'Starting'}</dd></div>
          </dl>
        </div>
        <div className="handoff-box">
          <div className="placeholder-box"><span>Stripe-hosted ACH checkout</span></div>
          {registration?.checkoutUrl && <a className="button" href={registration.checkoutUrl}>Continue to secure ACH checkout</a>}
          {registration?.ticketUrl && <a className="button secondary" href={registration.ticketUrl}>Open your QR ticket</a>}
          {!registration?.checkoutUrl && !registration?.ticketUrl && <small>This page updates automatically as the durable registration progresses.</small>}
        </div>
      </section>
      {error && <p className="notice error" role="alert">{error}</p>}
      <a className="back-link" href="/">Return to event details</a>
    </PageShell>
  );
}

function TicketPage({ token }: { token: string }) {
  const [ticket, setTicket] = useState<TicketView>();
  const [error, setError] = useState('');
  useEffect(() => {
    let active = true;
    void getTicket({ path: { ticketToken: token } }).then((result) => {
      if (!active) return;
      if (result.data) setTicket(result.data);
      else setError(errorMessage(result.error, 'Ticket is not available yet.'));
    });
    return () => { active = false; };
  }, [token]);

  return (
    <PageShell>
      <div className="page-heading">
        <p className="eyebrow">PRIVATE TICKET LINK</p>
        <h1>Your event ticket</h1>
        <p>{ticket?.checkedIn ? `Checked in ${formatDate(ticket.checkedInAt ?? '', ticket.event.timezone)}` : 'Present this QR code at event check-in.'}</p>
      </div>
      {ticket && <section className="ticket-card">
        <div className="ticket-copy">
          <span className="status-label">{ticket.checkedIn ? 'Checked in' : 'Payment confirmed'}</span>
          <h2>{ticket.event.name}</h2>
          <dl className="details-list">
            <div><dt>Attendee</dt><dd>{ticket.attendeeName}</dd></div>
            <div><dt>Date</dt><dd>{formatDate(ticket.event.startsAt, ticket.event.timezone)}</dd></div>
            <div><dt>Location</dt><dd>{ticket.event.location}</dd></div>
            <div><dt>Ticket code</dt><dd>{ticket.ticketCode}</dd></div>
          </dl>
        </div>
        <div className="qr-code"><img src={ticket.qrCodeDataUrl} alt={`QR ticket ${ticket.ticketCode}`} /></div>
      </section>}
      {!ticket && !error && <div className="panel">Loading ticket…</div>}
      {error && <p className="notice error" role="alert">{error}</p>}
      <div className="primary-actions">
        <button type="button" onClick={() => window.print()} disabled={!ticket}>Save or print ticket</button>
        <a className="text-link" href="/">Return to event page</a>
      </div>
      <p className="security-note">Keep this link private. The QR code grants access to the ticket but contains no attendee or payment information.</p>
    </PageShell>
  );
}

function CheckInPage() {
  const [ticketValue, setTicketValue] = useState('');
  const [result, setResult] = useState<CheckInView>();
  const [error, setError] = useState('');
  const [cameraOpen, setCameraOpen] = useState(false);
  const videoRef = useRef<HTMLVideoElement>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const animationRef = useRef<number>(0);

  const submitToken = useCallback(async (rawValue: string) => {
    const token = ticketToken(rawValue);
    if (!token) {
      setError('Enter a ticket code or private ticket URL.');
      return;
    }
    setError('');
    setResult(undefined);
    const response = await checkInTicket({
      headers: { 'X-Event-Permissions': 'checkin.scan' },
      body: { ticketToken: token },
    });
    if (response.data) setResult(response.data);
    else setError(errorMessage(response.error, 'Unable to check in this ticket.'));
  }, []);

  const stopCamera = useCallback(() => {
    window.cancelAnimationFrame(animationRef.current);
    streamRef.current?.getTracks().forEach((track) => track.stop());
    streamRef.current = null;
    setCameraOpen(false);
  }, []);

  useEffect(() => stopCamera, [stopCamera]);

  async function openCamera() {
    const Detector = (window as unknown as { BarcodeDetector?: new (options: { formats: string[] }) => { detect(source: HTMLVideoElement): Promise<Array<{ rawValue: string }>> } }).BarcodeDetector;
    if (!Detector) {
      setError('QR camera scanning is not supported by this browser. Paste the private ticket URL below.');
      return;
    }
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: { ideal: 'environment' } }, audio: false });
      streamRef.current = stream;
      setCameraOpen(true);
      const video = videoRef.current;
      if (!video) return;
      video.srcObject = stream;
      await video.play();
      const detector = new Detector({ formats: ['qr_code'] });
      const scan = async () => {
        try {
          const codes = await detector.detect(video);
          if (codes[0]?.rawValue) {
            setTicketValue(codes[0].rawValue);
            stopCamera();
            await submitToken(codes[0].rawValue);
            return;
          }
        } catch { /* keep scanning transient frames */ }
        animationRef.current = window.requestAnimationFrame(() => { void scan(); });
      };
      void scan();
    } catch {
      setError('Camera access was not available. Paste the private ticket URL below.');
      stopCamera();
    }
  }

  return (
    <PageShell>
      <div className="page-heading">
        <p className="eyebrow">STAFF ONLY</p>
        <h1>Event check-in</h1>
        <p>Production access requires an authenticated staff account with the <code>checkin.scan</code> permission.</p>
      </div>
      <section className="two-column check-in-layout">
        <div className="panel">
          <h2>Scan a ticket</h2>
          <div className="camera-placeholder">
            <video ref={videoRef} playsInline muted hidden={!cameraOpen} />
            {!cameraOpen && <><span>Camera preview</span><small>The rear camera opens only after staff request it.</small></>}
          </div>
          <button type="button" onClick={cameraOpen ? stopCamera : openCamera}>{cameraOpen ? 'Close camera' : 'Open camera'}</button>
        </div>
        <form className="panel" onSubmit={(event) => { event.preventDefault(); void submitToken(ticketValue); }}>
          <h2>Manual entry</h2>
          <label className="field" htmlFor="ticket-value">
            <span>Ticket code or private ticket URL</span>
            <input id="ticket-value" value={ticketValue} onChange={(event) => setTicketValue(event.target.value)} placeholder="Scan or paste a ticket" />
          </label>
          <button type="submit">Check in attendee</button>
          {result && <div className={`result-placeholder ${result.status === 'checked_in' ? 'success' : ''}`} role="status">
            <strong>{result.status === 'checked_in' ? 'Checked in' : 'Already checked in'}</strong>
            <span>{result.attendeeName} · {result.ticketCode}</span>
            <small>{new Date(result.checkedInAt).toLocaleString()}</small>
          </div>}
          {error && <p className="notice error" role="alert">{error}</p>}
        </form>
      </section>
      <a className="back-link" href="/">Leave staff check-in</a>
    </PageShell>
  );
}

function CurrentPage() {
  const path = window.location.pathname.replace(/\/+$/, '') || '/';
  if (path === '/register') return <RegistrationPage />;
  const payment = path.match(/^\/registration\/([^/]+)\/payment$/);
  if (payment) return <PaymentPage token={decodeURIComponent(payment[1])} />;
  const ticket = path.match(/^\/tickets\/([^/]+)$/);
  if (ticket) return <TicketPage token={decodeURIComponent(ticket[1])} />;
  if (path === '/check-in') return <CheckInPage />;
  return <EventPage />;
}

function ticketToken(value: string) {
  const trimmed = value.trim();
  try {
    const parsed = new URL(trimmed);
    const match = parsed.pathname.match(/^\/tickets\/([^/]+)$/);
    return match ? decodeURIComponent(match[1]) : '';
  } catch {
    return trimmed;
  }
}

function formatMoney(minor: number, currency: string) {
  return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(minor / 100);
}

function formatDate(value: string, timezone: string) {
  if (!value) return 'To be announced';
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'long', timeStyle: 'short', timeZone: timezone }).format(new Date(value));
}

function statusLabel(status: RegistrationStatus) {
  const labels: Record<RegistrationStatus, string> = {
    starting: 'Starting', reserved: 'Seat reserved', checkout_ready: 'Checkout ready', payment_pending: 'ACH payment pending',
    paid: 'Payment confirmed', ticket_emailed: 'Ticket emailed', sold_out: 'Sold out', payment_failed: 'Payment failed',
    expired: 'Checkout expired', cancelled: 'Cancelled', needs_attention: 'Needs staff attention',
  };
  return labels[status];
}

function isTerminalRegistration(status: RegistrationStatus) {
  return ['ticket_emailed', 'sold_out', 'payment_failed', 'expired', 'cancelled'].includes(status);
}

function errorMessage(error: unknown, fallback: string) {
  if (error && typeof error === 'object' && 'message' in error && typeof error.message === 'string') return error.message;
  return fallback;
}

export function App() {
  return <CurrentPage />;
}
