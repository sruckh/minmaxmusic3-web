// Lookup is a draft operation, never a generation. The selected file and its
// results live only on this page; draft.js owns every persisted preference.
function mm3Cover() {
  return {
    lookupAvailable: !!window.MM3_LYRICS_ENABLED,
    sourceFile: null, lookingUp: false, lookupMessage: '',
    lyricCandidates: [], lyricChoice: '0', lookupAbort: null, lookupSerial: 0,

    cancelLyricsLookup() {
      this.lookupSerial++;
      if (this.lookupAbort) this.lookupAbort.abort();
      this.lookupAbort = null;
      this.lookingUp = false;
      this.lookupMessage = 'Lookup cancelled. You can supply words or leave them blank for transcription.';
    },
    resetRecording() {
      this.cancelLyricsLookup();
      this.sourceFile = null;
      this.lyricCandidates = [];
      this.lookupMessage = '';
      if (this.$refs.sourceUpload) this.$refs.sourceUpload.value = '';
    },
    recordingChanged(file) {
      this.cancelLyricsLookup();
      this.lyricCandidates = [];
      this.lookupMessage = '';
      this.sourceFile = file || null;
      if (file && (file.size === 0 || file.size > 64 * 1024 * 1024)) {
        this.lookupMessage = 'Choose a nonempty recording under 64 MB.';
        this.sourceFile = null;
        this.$refs.sourceUpload.value = '';
        return;
      }
      if (this.findLyrics && this.sourceFile) this.lookupLyrics();
    },
    lookupPreferenceChanged() {
      if (this.findLyrics && this.sourceFile) this.lookupLyrics();
      else {
        this.cancelLyricsLookup();
        this.lyricCandidates = [];
        if (!this.sourceFile) this.lookupMessage = 'Choose a recording to find its lyrics.';
      }
    },
    matchLabel(match) {
      return [match.artist, match.title, match.version, match.album].filter(Boolean).join(' — ');
    },
    selectedLyrics() {
      const match = this.lyricCandidates[Number(this.lyricChoice)];
      return match && !match.instrumental ? match.lyrics : '';
    },
    describeLyricsMatch() {
      const match = this.lyricCandidates[Number(this.lyricChoice)];
      if (!match) return;
      this.lookupMessage = match.instrumental ? 'This match is marked instrumental; no lyrics were found.'
        : match.lyrics ? 'Lyrics found. Choose Use these lyrics, then review the words below.'
        : 'Recording identified, but no usable lyrics were found. You can supply them or use transcription.';
    },
    useFoundLyrics() {
      const found = this.selectedLyrics();
      if (!found) return;
      if (this.lyrics.trim() && !confirm('Replace the lyrics you have typed with this match?')) return;
      this.lyrics = found;
      this.lookupMessage = 'Lyrics added from LRCLIB. Review and edit them before generating.';
    },
    async lookupLyrics() {
      if (!this.isCover || !this.findLyrics || !this.sourceFile || !this.lookupAvailable) return;
      this.cancelLyricsLookup();
      const serial = this.lookupSerial;
      const wordsBefore = this.lyrics;
      const abort = new AbortController();
      this.lookupAbort = abort;
      this.lookingUp = true;
      this.lyricCandidates = [];
      this.lookupMessage = 'Identifying the recording and looking for lyrics…';
      const body = new FormData();
      body.append('source_upload', this.sourceFile);
      body.append('find_lyrics', '1');
      try {
        const response = await fetch('/lyrics/lookup', { method: 'POST', body, signal: abort.signal });
        const data = await response.json();
        if (serial !== this.lookupSerial) return;
        if (!response.ok) {
          this.lookupMessage = data.error || 'Lyrics lookup failed. You can still generate without it.';
          return;
        }
        this.lyricCandidates = data.candidates || [];
        this.lyricChoice = String(data.autoSelect >= 0 ? data.autoSelect : 0);
        if (!this.lyricCandidates.length) {
          this.lookupMessage = 'No recording match found. Supply the words or leave them blank for transcription.';
        } else if (data.autoSelect >= 0 && !wordsBefore.trim() && this.lyrics === wordsBefore && this.selectedLyrics()) {
          this.lyrics = this.selectedLyrics();
          this.lookupMessage = 'Lyrics added from LRCLIB. Review the recording match and words before generating.';
        } else {
          this.describeLyricsMatch();
        }
        if (data.warnings && data.warnings.length) this.lookupMessage += ' ' + data.warnings.join(' ');
      } catch (error) {
        if (serial === this.lookupSerial && error.name !== 'AbortError') {
          this.lookupMessage = 'Lyrics lookup failed. Retry or generate with your own words or transcription.';
        }
      } finally {
        if (serial === this.lookupSerial) {
          this.lookingUp = false;
          this.lookupAbort = null;
        }
      }
    }
  };
}
