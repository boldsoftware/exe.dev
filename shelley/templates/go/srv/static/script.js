// Handle SSH copy link
document.querySelectorAll('.ssh-copy').forEach(function(link) {
  link.addEventListener('click', function(e) {
    e.preventDefault();
    var text = this.getAttribute('data-copy');
    navigator.clipboard.writeText(text).then(function() {
      var feedback = document.getElementById('copiedFeedback');
      feedback.classList.add('show');
      setTimeout(function() {
        feedback.classList.remove('show');
      }, 2000);
    }).catch(function(err) {
      console.error('Failed to copy:', err);
    });
  });
});
