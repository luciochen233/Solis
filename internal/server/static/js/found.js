// "I found this item" form.
//   - "Where is the item" asks the browser for the finder's location and
//     attaches it to the report; nothing is sent until they press the main
//     button, and declining leaves the report location-free.
//   - The main button sends the report (message, contact details and the
//     location if one was attached).
(function () {
    var form = document.getElementById('found-form');
    if (!form) return;
    var submit = document.getElementById('found-submit');
    var locate = document.getElementById('found-locate');
    var status = document.getElementById('found-locate-status');

    function say(key) {
        if (status && locate) status.textContent = locate.getAttribute('data-' + key) || '';
    }

    if (locate) {
        if (!navigator.geolocation) {
            locate.disabled = true;
            say('unsupported');
        }
        locate.addEventListener('click', function () {
            locate.disabled = true;
            say('locating');
            navigator.geolocation.getCurrentPosition(function (pos) {
                document.getElementById('found-gps-lat').value = pos.coords.latitude;
                document.getElementById('found-gps-lon').value = pos.coords.longitude;
                document.getElementById('found-gps-accuracy').value = pos.coords.accuracy;
                locate.textContent = locate.getAttribute('data-done-label') || locate.textContent;
                say('located');
            }, function () {
                locate.disabled = false;
                say('denied');
            }, { enableHighAccuracy: true, timeout: 15000, maximumAge: 0 });
        });
    }

    form.addEventListener('submit', function () {
        if (submit) {
            submit.disabled = true;
            submit.textContent = submit.getAttribute('data-sending') || submit.textContent;
        }
    });
})();
