// Turning machine values into Indonesian a person can read.
//
// Interface text is Indonesian throughout, which includes the numbers: a decimal
// comma, and month names rather than a bare ISO timestamp.

const dateTime = new Intl.DateTimeFormat('id-ID', {
	day: 'numeric',
	month: 'short',
	year: 'numeric',
	hour: '2-digit',
	minute: '2-digit'
});

const dateOnly = new Intl.DateTimeFormat('id-ID', {
	day: 'numeric',
	month: 'short',
	year: 'numeric'
});

const oneDecimal = new Intl.NumberFormat('id-ID', { maximumFractionDigits: 1 });

/** @param {string} iso */
export function formatDateTime(iso) {
	if (!iso) return '';
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return '';
	return dateTime.format(d).replace(/\./g, '.');
}

/** @param {string} iso */
export function formatDate(iso) {
	if (!iso) return '';
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return '';
	return dateOnly.format(d);
}

// How long ago, in the units people actually say out loud.
//
// Stops at "hari" and hands over to the date: "47 hari lalu" is a number nobody
// converts in their head, while "23 Jul 2026" is immediately placed.
/** @param {string} iso */
export function formatRelative(iso) {
	if (!iso) return '';
	const then = new Date(iso);
	if (Number.isNaN(then.getTime())) return '';

	const seconds = Math.round((Date.now() - then.getTime()) / 1000);
	if (seconds < 0) return formatDateTime(iso);
	if (seconds < 45) return 'baru saja';
	if (seconds < 90) return 'semenit lalu';

	const minutes = Math.round(seconds / 60);
	if (minutes < 60) return `${minutes} menit lalu`;

	const hours = Math.round(minutes / 60);
	if (hours < 24) return `${hours} jam lalu`;

	const days = Math.round(hours / 24);
	if (days === 1) return 'kemarin';
	if (days < 7) return `${days} hari lalu`;

	return formatDate(iso);
}

/** @param {number} bytes */
export function formatSize(bytes) {
	if (bytes === null || bytes === undefined) return '';
	if (bytes < 1024) return `${bytes} B`;

	const units = ['KB', 'MB', 'GB', 'TB'];
	let value = bytes / 1024;
	let unit = 0;
	while (value >= 1024 && unit < units.length - 1) {
		value /= 1024;
		unit++;
	}
	return `${oneDecimal.format(value)} ${units[unit]}`;
}

/** @param {number} n @param {string} singular */
export function count(n, singular) {
	// Indonesian does not inflect for plural, so the number carries it alone.
	return `${n} ${singular}`;
}

/** @param {string} path */
export function baseName(path) {
	if (!path) return '';
	const parts = path.split(/[\\/]/);
	return parts[parts.length - 1] || path;
}

/** @param {string} path */
export function dirName(path) {
	if (!path) return '';
	const parts = path.split(/[\\/]/);
	parts.pop();
	return parts.join('\\') || path;
}

// An error from the Go side arrives as a string or an Error; either way the page
// shows it in place rather than in a dialog.
/** @param {unknown} err */
export function message(err) {
	if (!err) return '';
	if (typeof err === 'string') return err;
	if (err instanceof Error) return err.message;
	return String(err);
}
