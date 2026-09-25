import http from 'k6/http';
import { check, sleep } from 'k6';

const scenario = __ENV.SCENARIO || 'smoke';
const baseUrl = __ENV.BASE_URL || 'http://controller:8088';
const path = __ENV.TARGET_PATH || '/work';
const profiles = {
  smoke: { vus: 1, duration: '10s' },
  baseline: { vus: 10, duration: '1m' },
  spike: { stages: [{ duration: '10s', target: 5 }, { duration: '20s', target: 50 }, { duration: '10s', target: 0 }] },
  soak: { vus: 10, duration: '10m' },
  failure: { vus: 5, duration: '30s' },
};

if (!profiles[scenario]) {
  throw new Error(`unknown scenario: ${scenario}`);
}

const failure = scenario === 'failure';
export const options = {
  ...profiles[scenario],
  thresholds: failure
    ? { http_req_failed: ['rate>0.1', 'rate<0.9'] }
    : { http_req_failed: ['rate<0.01'], http_req_duration: ['p(95)<500'] },
};

export default function () {
  const response = http.get(`${baseUrl}${path}`);
  check(response, { 'expected response': (r) => failure ? [200, 503].includes(r.status) : r.status === 200 });
  sleep(0.2);
}
