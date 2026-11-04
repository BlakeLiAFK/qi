'use strict';
const form = document.getElementById('login');
form.addEventListener('submit', async event => {
  event.preventDefault();
  const input = document.getElementById('token');
  const button = form.querySelector('button');
  const status = document.getElementById('status');
  button.disabled = true;
  try {
    const token = input.value;
    input.value = '';
    const response = await fetch('/api/login', {method:'POST', headers:{'Content-Type':'application/json','X-Qi-Request':'1'}, body:JSON.stringify({token}), credentials:'same-origin'});
    if (!response.ok) throw new Error(response.status === 429 ? 'Too many login attempts. Try again in one minute.' : 'Sign-in failed. Check your credential or contact the operator.');
    location.replace('/');
  } catch (error) { status.textContent = error.message; }
  finally { button.disabled = false; }
});
