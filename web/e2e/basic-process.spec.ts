import { expect, test } from '@playwright/test';

test('public page is backed by the live Dex event inventory', async ({ page, request }) => {
  const response = await request.get('/api/event');
  expect(response.ok()).toBeTruthy();
  const event = await response.json();
  expect(event.capacity).toBe(300);
  expect(event.remaining).toBe(300);

  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Event name to be announced' })).toBeVisible();
  await expect(page.getByText('300 of 300 places remain.')).toBeVisible();
});

for (const pageDefinition of [
  { path: '/register', heading: 'Register for the event' },
  { path: '/registration/sample-token/payment', heading: 'Complete your ACH payment' },
  { path: '/tickets/sample-ticket-token', heading: 'Your event ticket' },
  { path: '/check-in', heading: 'Event check-in' },
]) {
  test(`${pageDefinition.path} keeps its public route`, async ({ page }) => {
    await page.goto(pageDefinition.path);
    await expect(page.getByRole('heading', { name: pageDefinition.heading })).toBeVisible();
  });
}

test('check-in rejects callers without the staff permission', async ({ request }) => {
  const response = await request.post('/api/check-ins', {
    headers: { 'X-Event-Permissions': 'event.view' },
    data: { ticketToken: 'not-a-real-ticket-token-value' },
  });
  expect(response.status()).toBe(401);
});
