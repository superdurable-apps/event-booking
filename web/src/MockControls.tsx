import { useEffect, useState } from 'react';
import type { RegistrationView } from './api/generated/types.gen';

type MockAction = 'settle_payment' | 'fail_payment' | 'expire_payment' | 'email_review' | 'reset';

type MockControlView = {
  mode: 'mock';
  registration?: RegistrationView;
  staffToken: string;
};

export function MockControls({ registration, onRegistrationChange }: {
  registration: RegistrationView;
  onRegistrationChange: (registration: RegistrationView) => void;
}) {
  const [available, setAvailable] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('Use these controls to exercise asynchronous payment outcomes.');

  useEffect(() => {
    let active = true;
    void fetch(`/__mock__/control?registrationId=${encodeURIComponent(registration.registrationId)}`)
      .then((response) => { if (active) setAvailable(response.ok); })
      .catch(() => { if (active) setAvailable(false); });
    return () => { active = false; };
  }, [registration.registrationId]);

  if (!available) return null;

  async function runControl(action: MockAction) {
    setBusy(true);
    try {
      const response = await fetch('/__mock__/control', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ action, registrationId: registration.registrationId }),
      });
      const result = await response.json() as MockControlView | { message?: string };
      if (!response.ok) throw new Error('message' in result ? result.message : 'Mock control failed.');
      const control = result as MockControlView;
      if (control.registration) onRegistrationChange(control.registration);
      setMessage(`Applied ${action.replaceAll('_', ' ')}.`);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : 'Mock control failed.');
    } finally {
      setBusy(false);
    }
  }

  return (
    <details className="mock-controls" open>
      <summary>Mock controls</summary>
      <p>Local simulation for payment settlement and email outcomes.</p>
      <div className="mock-control-actions">
        <button disabled={busy || registration.paymentStatus === 'paid'} type="button" onClick={() => void runControl('settle_payment')}>Settle ACH payment</button>
        <button disabled={busy || registration.paymentStatus === 'paid'} type="button" onClick={() => void runControl('fail_payment')}>Fail payment</button>
        <button disabled={busy || registration.paymentStatus === 'paid'} type="button" onClick={() => void runControl('expire_payment')}>Expire checkout</button>
        <button disabled={busy} type="button" onClick={() => void runControl('email_review')}>Simulate email review</button>
      </div>
      <output aria-live="polite">{message}</output>
    </details>
  );
}
