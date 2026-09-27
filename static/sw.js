// Minimal service worker for PWA installability
// No offline caching — just enough to satisfy install requirements
self.addEventListener('fetch', function(event) {
  event.respondWith(fetch(event.request));
});
