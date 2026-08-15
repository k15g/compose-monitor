// Live updates and actions for the services pages.
//
// EventSource is used rather than a WebSocket because the traffic is one-way
// and the browser reconnects on its own — there is no backoff loop to write,
// and no ping/pong to keep alive. Every connection begins with a full
// snapshot, so a reconnect after a drop needs no replay of what was missed.
(function () {
	"use strict";

	var rows = document.getElementById("service-rows");
	var empty = document.getElementById("empty");
	// The front page's listing carries this; the services page's <tbody> does
	// not. It decides both what the empty message means and which shape of row
	// to ask the stream for.
	var runningOnly = rows !== null && rows.classList.contains("running-only");
	var connection = document.getElementById("connection");

	// --- confirmation dialog ------------------------------------------------
	//
	// Remove controls are links to a server-rendered confirmation page. htmx
	// fetches that page's fragment into the dialog instead of navigating, so
	// the question is the same either way and the modal never invents it.

	var dialog = document.getElementById("confirm-dialog");

	if (dialog) {
		document.body.addEventListener("htmx:afterSwap", function (event) {
			if (event.detail.target.id === "confirm-body") {
				dialog.showModal();
			}
		});

		// Cancel is a real link to the page behind the dialog. While the dialog
		// is open there is nowhere to go — closing it is the whole action.
		dialog.addEventListener("click", function (event) {
			if (event.target.closest("[data-close-dialog]")) {
				event.preventDefault();
				dialog.close();
			}
			// A click on the backdrop lands on the dialog itself.
			if (event.target === dialog) {
				dialog.close();
			}
		});
	}

	// --- actions ------------------------------------------------------------
	//
	// Start and stop are real form posts, so both work with JavaScript off.
	// This listener is on the document rather than on the table because the
	// detail page has the same buttons and no table.

	document.addEventListener("submit", function (event) {
		var form = event.target.closest(".action-form");
		if (!form) {
			return;
		}

		// Only the live list is patched in place. Elsewhere the plain form post
		// and its redirect are left to happen, which is what reloads a detail
		// page — or leaves it, when the container it described is gone.
		if (!rows || !rows.contains(form)) {
			return;
		}
		event.preventDefault();

		var button = form.querySelector("button");
		// The button holds an icon beside its text, so only the text is
		// touched — writing to the button itself would take the icon with it.
		var caption = button ? button.querySelector("span") : null;
		var label = caption ? caption.textContent : "";
		if (button) {
			button.disabled = true;
			if (caption) {
				caption.textContent = busyLabel(label);
			}
		}

		fetch(form.action, {
			method: "POST",
			headers: { "X-Requested-With": "fetch" },
		}).then(function (response) {
			if (response.ok) {
				// Nothing to do: the row is about to be replaced or removed by
				// the event the action produced.
				return;
			}
			restore();
			return response.text().then(function (text) {
				window.alert("The action failed: " + (text.trim() || response.status));
			});
		}).catch(restore);

		function restore() {
			if (button) {
				button.disabled = false;
			}
			if (caption) {
				caption.textContent = label;
			}
		}
	});

	function busyLabel(label) {
		return label === "Start" ? "Starting…" : "Stopping…";
	}

	// --- log, fetched when its panel is opened -------------------------------
	//
	// A log costs a request to the daemon and can be large, and most visits to
	// a detail page are not about it. The panel carries the URL to fill itself
	// from and does so the first time it is opened.

	var logPanel = document.querySelector(".log-panel");

	if (logPanel) {
		var logBody = logPanel.querySelector("[data-log-body]");
		var logLoaded = false;

		logPanel.addEventListener("toggle", function () {
			if (logPanel.open && !logLoaded) {
				loadLog(0);
			}
		});

		// The size controls re-fetch rather than reload the page, which would
		// close the panel that was just opened.
		logPanel.addEventListener("click", function (event) {
			var button = event.target.closest("[data-log-tail]");
			if (button) {
				loadLog(button.dataset.logTail);
			}
		});

		function loadLog(tail) {
			logLoaded = true;
			var url = logPanel.dataset.logUrl + (tail ? "?tail=" + tail : "");

			fetch(url, { headers: { "HX-Request": "true" } })
				.then(function (response) {
					if (!response.ok) {
						throw new Error(String(response.status));
					}
					return response.text();
				})
				.then(function (html) {
					logBody.innerHTML = html;
				})
				.catch(function (err) {
					logBody.textContent = "The log could not be loaded: " + err.message;
					logLoaded = false;
				});
		}
	}

	// --- the update stream --------------------------------------------------
	//
	// Opened on every page, not only the ones with a table to patch: the dot in
	// the header says whether this page is receiving updates, and a dot that
	// sat at "connecting" forever on a detail page would be a lie. The cost is
	// one idle connection per open tab.

	function setConnection(state, text) {
		if (!connection) {
			return;
		}
		connection.dataset.state = state;
		connection.title = text;

		var label = connection.querySelector(".visually-hidden");
		if (label) {
			label.textContent = text;
		}
	}

	// A <template> parses a fragment in a context that accepts anything, which
	// is what lets a bare <tr> become a node — assigning the same markup to a
	// <div> silently drops the row and leaves the cells behind as text.
	function parseRow(html) {
		var template = document.createElement("template");
		template.innerHTML = html.trim();
		return template.content.firstElementChild;
	}

	function find(id) {
		return rows.querySelector('[data-service-id="' + id + '"]');
	}

	// htmx wires up what was in the document at load and what htmx itself
	// inserts — and nothing else. These rows arrive over the event stream and
	// are inserted with plain DOM calls, so without this their Remove links
	// keep their href and lose their hx-get: clicking one navigates to the
	// confirmation page instead of opening it over this one.
	//
	// The sync frame replaces the whole table a moment after load, so this is
	// not an edge case — it is every row on the page.
	function activate(element) {
		if (window.htmx) {
			window.htmx.process(element);
		}
	}

	// Insert keeping the order the server would have rendered: rows carry the
	// same sort key the server sorts by, so a row that arrives over the stream
	// lands where a page reload would have put it.
	function insert(row) {
		var key = row.dataset.sort;
		var children = rows.children;
		for (var i = 0; i < children.length; i++) {
			if (children[i].dataset.sort > key) {
				rows.insertBefore(row, children[i]);
				return;
			}
		}
		rows.appendChild(row);
	}

	function upsert(id, html, notable) {
		var row = parseRow(html);
		if (!row) {
			return;
		}
		var existing = find(id);
		if (existing) {
			existing.remove();
		}
		insert(row);
		activate(row);

		// Only when something happened. The row is also redrawn when the
		// elapsed time in its status ticks over, and flashing for that would
		// have every row blinking on a timer with nothing to look at.
		if (notable) {
			flash(row);
		}
	}

	function flash(row) {
		row.classList.add("flash");
		setTimeout(function () {
			row.classList.remove("flash");
		}, 1200);
	}

	// The totals are recomputed from the rows rather than sent as their own
	// event: the rows are the truth, and a separate count could disagree with
	// them after a dropped event.
	function recount() {
		if (!rows) {
			return;
		}
		var all = rows.querySelectorAll(".service");
		var online = 0;
		for (var i = 0; i < all.length; i++) {
			if (all[i].dataset.online === "true") {
				online++;
			}
		}
		// Online and offline are the two worth reading; their sum is not news.
		set("count-online", online);
		set("count-offline", all.length - online);

		if (empty) {
			// On a table showing only what is running, having rows is not the
			// same as having anything to show.
			empty.hidden = (runningOnly ? online : all.length) > 0;
		}
	}

	function set(id, value) {
		var element = document.getElementById(id);
		if (element) {
			element.textContent = String(value);
		}
	}

	// The stream is told which shape this page wants its rows in, because the
	// server renders them and a service looks different on the listing than in
	// the table.
	var view = runningOnly ? "overview" : "services";
	var source = new EventSource("/events?view=" + view);

	source.addEventListener("open", function () {
		setConnection("live", "receiving live updates");
	});

	// EventSource reports a dropped connection and a failed reconnect the same
	// way, and retries by itself. Saying "reconnecting" is honest for both.
	source.addEventListener("error", function () {
		setConnection("down", "not receiving updates — reconnecting");
	});

	source.addEventListener("sync", function (event) {
		setConnection("live", "receiving live updates");
		if (!rows) {
			return;
		}

		var payload = JSON.parse(event.data);
		rows.replaceChildren();
		payload.services.forEach(function (service) {
			var row = parseRow(service.html);
			if (row) {
				rows.appendChild(row);
			}
		});
		activate(rows);
		recount();
	});

	source.addEventListener("change", function (event) {
		if (!rows) {
			return;
		}

		var payload = JSON.parse(event.data);
		if (payload.action === "removed") {
			var existing = find(payload.id);
			if (existing) {
				existing.remove();
			}
		} else {
			// added and updated are the same upsert. They are not
			// distinguished because a client that subscribed just before the
			// snapshot was read can legitimately receive an "added" for a row
			// it already has.
			upsert(payload.id, payload.html, payload.notable);
		}
		recount();
	});
})();
