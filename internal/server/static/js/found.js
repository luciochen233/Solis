// "I found this item" form: optionally attach the finder's location, then send.
// The browser asks the finder for permission; if they decline, or the lookup
// takes too long, the report is sent without a location.
(function () {
    var form = document.getElementById('found-form');
    if (!form) return;
    var started = false;

    form.addEventListener('submit', function (e) {
        if (started) return;
        started = true;
        e.preventDefault();

        var btn = document.getElementById('found-submit');
        if (btn) {
            btn.disabled = true;
            btn.textContent = btn.getAttribute('data-sending') || btn.textContent;
        }

        var sent = false;
        function send() {
            if (sent) return;
            sent = true;
            form.submit();
        }

        var share = document.getElementById('found-share-location');
        if (!share || !share.checked || !navigator.geolocation) {
            send();
            return;
        }

        var timer = setTimeout(send, 12000);
        navigator.geolocation.getCurrentPosition(function (pos) {
            clearTimeout(timer);
            document.getElementById('found-gps-lat').value = pos.coords.latitude;
            document.getElementById('found-gps-lon').value = pos.coords.longitude;
            document.getElementById('found-gps-accuracy').value = pos.coords.accuracy;
            send();
        }, function () {
            clearTimeout(timer);
            send();
        }, { enableHighAccuracy: true, timeout: 10000, maximumAge: 0 });
    });
})();
