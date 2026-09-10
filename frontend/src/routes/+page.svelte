<script>
	import { ListAssets } from '$lib/wailsjs/go/cmd/App.js';
	import { formatRelative, message, count } from '$lib/format.js';
	import { statusStore } from '$lib/status.svelte.js';

	/** @type {import('$lib/wailsjs/go/models').cmd.AssetCardView[]} */
	let assets = $state([]);
	let loaded = $state(false);
	let problem = $state('');

	async function load() {
		try {
			assets = (await ListAssets()) ?? [];
			problem = '';
		} catch (err) {
			problem = message(err);
		} finally {
			loaded = true;
		}
	}

	// Polled rather than pushed. A new version appearing on its own is pleasant;
	// a notification telling you about it is an interruption.
	$effect(() => {
		load();
		const id = setInterval(load, 2000);
		return () => clearInterval(id);
	});

	const status = $derived(statusStore.value);
	const hasFolders = $derived((status.folders ?? []).length > 0);
</script>

{#if problem}
	<p class="note">{problem}</p>
{/if}

{#if loaded && assets.length === 0}
	<section class="empty">
		{#if !hasFolders}
			<h1>Belum ada folder yang diawasi</h1>
			<p class="muted">
				Pilih folder tempat kamu menyimpan kerjaan desain. CIT akan mengamatinya dan mencatat
				setiap penyimpanan sebagai satu versi — tanpa memindahkan atau mengubah berkasmu.
			</p>
			<p class="faint">
				Gunakan tombol <em>Belum ada folder diawasi</em> di atas untuk memilihnya.
			</p>
		{:else}
			<h1>Belum ada yang tercatat</h1>
			<p class="muted">
				Folder sudah diawasi. Versi pertama muncul di sini beberapa detik setelah kamu
				menyimpan sebuah berkas — CIT menunggu sampai penyimpanannya benar-benar selesai
				sebelum mencatat, supaya satu Ctrl+S jadi tepat satu versi.
			</p>
		{/if}
	</section>
{:else if assets.length > 0}
	<div class="head">
		<h1>Karya</h1>
		<span class="faint">
			{count(status.assets, 'karya')} · {count(status.versions, 'versi')}
		</span>
	</div>

	<ul class="grid">
		{#each assets as asset (asset.id)}
			<li>
				<a class="card" href={`/karya/${asset.id}`}>
					<div class="thumb">
						{#if asset.thumbUrl}
							<img src={asset.thumbUrl} alt="" loading="lazy" />
						{:else}
							<div class="no-thumb faint">tanpa gambar kecil</div>
						{/if}

						{#if asset.alphaFlattened}
							<!-- A white logo on transparency, composited onto white, is an empty
							     rectangle. Saying so is the difference between "nothing was
							     drawn" and "the user's work was blank". -->
							<span class="tag set-aside on-thumb">transparansi diratakan</span>
						{/if}
						{#if !asset.contentPresent}
							<span class="tag set-aside on-thumb">berkas tidak lagi disimpan</span>
						{/if}
					</div>

					<div class="meta">
						<span class="title">{asset.name}</span>
						<span class="faint sub">
							{count(asset.versions, 'versi')}
							{#if asset.files > 1}
								· {count(asset.files, 'berkas')}
							{/if}
							· {formatRelative(asset.updatedAt)}
						</span>
					</div>
				</a>
			</li>
		{/each}
	</ul>
{/if}

<style>
	.head {
		display: flex;
		align-items: baseline;
		gap: 12px;
		margin-bottom: 16px;
	}

	.grid {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
		gap: 16px;
	}

	.card {
		display: flex;
		flex-direction: column;
		background: var(--surface);
		border: 1px solid var(--line);
		border-radius: var(--radius);
		overflow: hidden;
		box-shadow: var(--shadow);
		transition: border-color 120ms ease, transform 120ms ease;
	}

	.card:hover {
		border-color: var(--line-strong);
		transform: translateY(-1px);
	}

	.thumb {
		position: relative;
		aspect-ratio: 4 / 3;
		background: var(--surface-sunken);
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.thumb img {
		width: 100%;
		height: 100%;
		object-fit: contain;
	}

	.no-thumb {
		font-size: 11px;
	}

	.on-thumb {
		position: absolute;
		left: 6px;
		bottom: 6px;
		max-width: calc(100% - 12px);
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.meta {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 10px 12px 12px;
		border-top: 1px solid var(--line);
	}

	.title {
		font-weight: 500;
		overflow-wrap: anywhere;
	}

	.sub {
		font-size: 12px;
	}

	.empty {
		max-width: 60ch;
		margin: 10vh auto 0;
		text-align: center;
	}

	.empty h1 {
		margin-bottom: 10px;
	}

	.empty p {
		margin: 0 0 10px;
	}
</style>
