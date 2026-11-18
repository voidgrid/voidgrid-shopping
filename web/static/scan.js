// Barcode scanning with the browser's own BarcodeDetector (Chrome on Android).
// Browsers without it keep the typed-number box, which always works.
(function () {
  var box = document.querySelector('[data-scan]');
  if (!box) { return; }
  var start = box.querySelector('.scanstart');
  var video = box.querySelector('.scanview');
  var msg = box.querySelector('.scanmsg');
  var input = box.querySelector('input[name="barcode"]');
  var form = box.querySelector('form');
  function say(t) { msg.textContent = t; msg.hidden = !t; }
  if (!('BarcodeDetector' in window) || !navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    start.hidden = true;
    say('This browser cannot scan barcodes. Type the number under the bars instead.');
    return;
  }
  var stream = null;
  var running = false;
  function stop() {
    running = false;
    if (stream) { stream.getTracks().forEach(function (t) { t.stop(); }); stream = null; }
    video.hidden = true;
  }
  start.addEventListener('click', function () {
    if (running) { stop(); say(''); return; }
    var detector = new window.BarcodeDetector({ formats: ['ean_13', 'ean_8', 'upc_a', 'upc_e'] });
    navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' }, audio: false }).then(function (s) {
      stream = s;
      video.srcObject = s;
      video.hidden = false;
      running = true;
      say('Hold the barcode in view.');
      return video.play();
    }).then(function () {
      (function tick() {
        if (!running) { return; }
        detector.detect(video).then(function (codes) {
          if (codes.length > 0 && /^[0-9]{8,14}$/.test(codes[0].rawValue)) {
            input.value = codes[0].rawValue;
            stop();
            form.submit();
            return;
          }
          window.setTimeout(tick, 250);
        }).catch(function () { window.setTimeout(tick, 500); });
      })();
    }).catch(function () {
      stop();
      say('The camera could not be opened. Allow camera access for this site, or type the number.');
    });
  });
  window.addEventListener('pagehide', stop);
})();
