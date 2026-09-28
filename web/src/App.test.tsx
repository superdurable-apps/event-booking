import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { App } from './App';
import { getEvent, getRegistration, getTicket } from './api/generated/sdk.gen';

vi.mock('./api/generated/sdk.gen', () => ({
  getEvent: vi.fn(),
  createRegistration: vi.fn(),
  getRegistration: vi.fn(),
  getTicket: vi.fn(),
  checkInTicket: vi.fn(),
}));

const event = {
  eventId: 'default', name: 'Community gathering', description: 'A durable event.',
  startsAt: '2027-01-02T02:00:00Z', timezone: 'America/Los_Angeles', location: 'Main hall',
  currency: 'USD', priceMinor: 7500, capacity: 300, reserved: 2, paid: 10, remaining: 288, registrationOpen: true,
};

function renderAt(path: string) {
  window.history.pushState({}, '', path);
  render(<App />);
}

describe('event registration UI', () => {
  beforeEach(() => {
    vi.mocked(getEvent).mockResolvedValue({ data: event, error: undefined });
    vi.mocked(getRegistration).mockResolvedValue({ data: undefined, error: { error: 'not_found', message: 'Not found' } });
    vi.mocked(getTicket).mockResolvedValue({ data: undefined, error: { error: 'not_found', message: 'Not found' } });
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
    window.history.pushState({}, '', '/');
  });

  it('loads the public event and live availability through the generated client', async () => {
    renderAt('/');
    expect(await screen.findByRole('heading', { name: 'Community gathering' })).toBeInTheDocument();
    expect(screen.getByText('288 of 300 places remain.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /register for the event/i })).toHaveAttribute('href', '/register');
  });

  it.each([
    ['/register', 'Register for the event'],
    ['/registration/sample-token/payment', 'Complete your ACH payment'],
    ['/check-in', 'Event check-in'],
  ])('renders %s at a stable URL', (path, heading) => {
    renderAt(path);
    expect(screen.getByRole('heading', { name: heading })).toBeInTheDocument();
  });

  it('renders a paid QR ticket returned by the generated client', async () => {
    vi.mocked(getTicket).mockResolvedValue({ data: {
      event, attendeeName: 'Ada Lovelace', ticketCode: 'ABC123', ticketUrl: 'https://events.example.test/tickets/token',
      qrCodeDataUrl: 'data:image/png;base64,cG5n', checkedIn: false,
    }, error: undefined });
    renderAt('/tickets/ticket-token');
    expect(await screen.findByText('Ada Lovelace')).toBeInTheDocument();
    expect(screen.getByRole('img', { name: /QR ticket ABC123/i })).toHaveAttribute('src', 'data:image/png;base64,cG5n');
  });
});
