import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MockControls } from './MockControls';
import type { RegistrationView } from './api/generated/types.gen';

const registration: RegistrationView = {
  registrationId: '00000000-0000-4000-8000-000000000001', displayCode: '00000001', attendeeName: 'Example Attendee',
  emailMasked: 'e******@example.com', state: 'payment_pending', paymentStatus: 'pending', ticketStatus: 'not_available', message: 'Pending',
  emailDeliveryStatus: 'not_started',
};

describe('MockControls', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true, json: () => Promise.resolve({ mode: 'mock', registration, staffToken: 'mock-staff-token-123' }),
    }));
  });

  afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

  it('appears only when the mock endpoint is available', async () => {
    render(<MockControls registration={registration} onRegistrationChange={vi.fn()} />);
    expect(await screen.findByText('Mock controls')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Settle ACH payment' })).toBeEnabled();
  });

  it('applies a settlement state returned by the mock API', async () => {
    const onChange = vi.fn();
    render(<MockControls registration={registration} onRegistrationChange={onChange} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Settle ACH payment' }));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(registration));
  });
});
