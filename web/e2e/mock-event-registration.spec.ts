import { expect, test } from '@playwright/test';

test.skip(process.env.E2E_MOCK !== 'true', 'requires the local mock server');

test('registers, settles ACH, opens a QR ticket, and checks in once', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 1, name: 'Event name TBD' })).toBeVisible();
  await page.getByRole('link', { name: 'Register for the event' }).click();
  await page.getByLabel('First name').fill('Ada');
  await page.getByLabel('Last name').fill('Lovelace');
  await page.getByLabel('Email address').fill('ada@example.com');
  await page.getByLabel('I agree to the event policies.').check();
  await page.getByRole('button', { name: 'Continue to secure ACH payment' }).click();

  await expect(page.getByRole('heading', { level: 1, name: 'Payment is processing' })).toBeVisible();
  await expect(page.getByText('Mock controls')).toBeVisible();
  await page.getByRole('button', { name: 'Settle ACH payment' }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'Your ticket is ready' })).toBeVisible();
  await page.getByRole('link', { name: 'Open electronic ticket' }).click();

  await expect(page.getByRole('img', { name: 'Ticket QR code' })).toBeVisible();
  const ticketURL = page.url();
  await page.goto('/check-in');
  await page.getByLabel('Staff access token').fill('mock-staff-token-123');
  await page.getByLabel('Ticket code or URL').fill(ticketURL);
  await page.getByRole('button', { name: 'Check in attendee' }).click();
  await expect(page.getByText('Check-in successful')).toBeVisible();
  await page.getByRole('button', { name: 'Check in attendee' }).click();
  await expect(page.getByText('Already checked in')).toBeVisible();
});

test('keeps the QR ticket usable when email delivery fails', async ({ page }) => {
  await page.goto('/register');
  await page.getByLabel('First name').fill('Katherine');
  await page.getByLabel('Last name').fill('Johnson');
  await page.getByLabel('Email address').fill('katherine@example.com');
  await page.getByLabel('I agree to the event policies.').check();
  await page.getByRole('button', { name: 'Continue to secure ACH payment' }).click();

  await page.getByRole('button', { name: 'Simulate email review' }).click();
  await expect(page.getByText('failed', { exact: true })).toBeVisible();
  await page.getByRole('link', { name: 'Open electronic ticket' }).click();
  await expect(page.getByText('Your ticket is valid online, but email delivery failed. Please contact the organizer.')).toBeVisible();
  await expect(page.getByRole('img', { name: 'Ticket QR code' })).toBeVisible();

  const ticketURL = page.url();
  await page.goto('/check-in');
  await page.getByLabel('Staff access token').fill('mock-staff-token-123');
  await page.getByLabel('Ticket code or URL').fill(ticketURL);
  await page.getByRole('button', { name: 'Check in attendee' }).click();
  await expect(page.getByText('Check-in successful')).toBeVisible();
});
