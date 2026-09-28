import '@testing-library/jest-dom/vitest';
import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { App } from './App';
import { getEvent, getRegistration, getTicket } from './api/generated/sdk.gen';

vi.mock('./api/generated/sdk.gen', () => ({
  getEvent: vi.fn(), getRegistration: vi.fn(), getTicket: vi.fn(), createRegistration: vi.fn(), checkInTicket: vi.fn(),
}));

const event = {
  id: 'event-tbd', name: 'Event name TBD', description: 'Description', dateTimeDisplay: 'To be announced',
  location: 'To be announced', priceDisplay: '$125', capacity: 300, reservedCount: 2, registrationOpen: true,
  registrationDeadlineDisplay: 'To be announced', organizerEmail: 'events@example.com',
};

beforeEach(() => {
  vi.mocked(getEvent).mockResolvedValue({ data: event, error: undefined, response: new Response(), request: undefined });
  vi.mocked(getRegistration).mockResolvedValue({ data: {
    registrationId: '00000000-0000-4000-8000-000000000001', displayCode: '00000001', attendeeName: 'Example Attendee',
    emailMasked: 'e******@example.com', state: 'payment_pending', paymentStatus: 'pending', ticketStatus: 'not_available',
    emailDeliveryStatus: 'not_started', message: 'Payment is pending.', checkoutUrl: 'https://checkout.stripe.test/example',
  }, error: undefined, response: new Response(), request: undefined });
  vi.mocked(getTicket).mockResolvedValue({ data: {
    registrationId: '00000000-0000-4000-8000-000000000001', displayCode: '00000001', attendeeName: 'Example Attendee',
    eventName: 'Event name TBD', dateTimeDisplay: 'To be announced', location: 'To be announced', qrImageUrl: '/qr.png',
    emailDeliveryStatus: 'sent', checkedIn: false,
  }, error: undefined, response: new Response(), request: undefined });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  window.history.replaceState({}, '', '/');
  vi.clearAllMocks();
});

describe('event registration site', () => {
  it.each([
    ['/', 'Event name TBD'], ['/register', 'Reserve your ticket'], ['/check-in', 'Ticket check-in'],
  ])('renders %s directly', async (path, heading) => {
    window.history.replaceState({}, '', path);
    render(<App />);
    expect(await screen.findByRole('heading', { level: 1, name: heading })).toBeInTheDocument();
  });

  it('loads a protected registration status', async () => {
    window.history.replaceState({}, '', '/registration/00000000-0000-4000-8000-000000000001#token=test-token');
    render(<App />);
    expect(await screen.findByRole('heading', { level: 1, name: 'Payment is processing' })).toBeInTheDocument();
    await waitFor(() => expect(getRegistration).toHaveBeenCalled());
    expect(screen.getByText('Example Attendee')).toBeInTheDocument();
  });

  it('polls a transient registration and stops when the ticket is ready', async () => {
    vi.useFakeTimers();
    const pending = {
      registrationId: '00000000-0000-4000-8000-000000000001', displayCode: '00000001', attendeeName: 'Example Attendee',
      emailMasked: 'e******@example.com', state: 'payment_pending' as const, paymentStatus: 'pending' as const,
      ticketStatus: 'not_available' as const, emailDeliveryStatus: 'not_started' as const,
      message: 'Payment is pending.', checkoutUrl: 'https://checkout.stripe.test/example',
    };
    const ready = {
      ...pending, state: 'ticket_ready' as const, paymentStatus: 'paid' as const, ticketStatus: 'ready' as const,
      message: 'Your electronic ticket is ready.', ticketUrl: 'http://127.0.0.1:8080/ticket/example',
    };
    vi.mocked(getRegistration).mockResolvedValueOnce({ data: pending, error: undefined, response: new Response(), request: undefined })
      .mockResolvedValue({ data: ready, error: undefined, response: new Response(), request: undefined });
    window.history.replaceState({}, '', '/registration/00000000-0000-4000-8000-000000000001#token=test-token');

    render(<App />);
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(screen.getByRole('heading', { level: 1, name: 'Payment is processing' })).toBeInTheDocument();
    expect(getRegistration).toHaveBeenCalledTimes(1);

    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    expect(screen.getByRole('heading', { level: 1, name: 'Your ticket is ready' })).toBeInTheDocument();
    expect(getRegistration).toHaveBeenCalledTimes(2);

    await act(async () => { await vi.advanceTimersByTimeAsync(4_000); });
    expect(getRegistration).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('button', { name: 'Refresh status' })).toBeEnabled();
  });

  it('loads a paid electronic ticket', async () => {
    window.history.replaceState({}, '', '/ticket/00000000-0000-4000-8000-000000000001?token=test-token');
    render(<App />);
    expect(await screen.findByRole('heading', { level: 1, name: 'You are registered' })).toBeInTheDocument();
    expect(screen.getByRole('img', { name: 'Ticket QR code' })).toBeInTheDocument();
    expect(screen.getByText('A copy was sent by email.')).toBeInTheDocument();
  });

  it('keeps the online ticket valid while reporting failed email delivery', async () => {
    vi.mocked(getTicket).mockResolvedValueOnce({ data: {
      registrationId: '00000000-0000-4000-8000-000000000001', displayCode: '00000001', attendeeName: 'Example Attendee',
      eventName: 'Event name TBD', dateTimeDisplay: 'To be announced', location: 'To be announced', qrImageUrl: '/qr.png',
      emailDeliveryStatus: 'failed', checkedIn: false,
    }, error: undefined, response: new Response(), request: undefined });
    window.history.replaceState({}, '', '/ticket/00000000-0000-4000-8000-000000000001?token=test-token');
    render(<App />);

    expect(await screen.findByText('Your ticket is valid online, but email delivery failed. Please contact the organizer.')).toBeInTheDocument();
    expect(screen.getByRole('img', { name: 'Ticket QR code' })).toBeInTheDocument();
    expect(screen.queryByText('A copy was sent by email.')).not.toBeInTheDocument();
  });
});
